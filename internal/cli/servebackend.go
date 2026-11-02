package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/provider"
	"github.com/Carudy/pai/internal/runner"
	"github.com/Carudy/pai/internal/session"
)

// The same mutex protects API reads, preparation, and recorder appends on both
// backends; a worker never owns the store or changes the process workspace.
type serveBackend struct {
	mu    sync.Mutex
	store session.Store
	cwd   string
}

// serviceQueueLimit bounds the pending-work list in a service view; it is for
// inspection, not the delivery path.
const serviceQueueLimit = 20

func serveMeta(m session.Meta) runner.Meta {
	return runner.Meta{Name: m.Name, ID: m.ID, Role: m.Role, Model: m.Model, Cwd: m.Cwd,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, Mode: m.Mode, State: m.State,
		NextWake: m.NextWake, TokenScale: m.TokenScale(), Turns: m.Turns}
}

// The serve backend is the production implementation of the optional resident
// ports. Events use the immutable session id; metadata stays name-keyed.
var _ runner.ServiceEventBackend = (*serveBackend)(nil)
var _ runner.ServiceWatcherBackend = (*serveBackend)(nil)
var _ runner.ServiceStateBackend = (*serveBackend)(nil)
var _ runner.ServiceBackend = (*serveBackend)(nil)
var _ runner.ServiceCreator = (*serveBackend)(nil)
var _ runner.GroupBackend = (*serveBackend)(nil)
var _ runner.TaskBackend = (*serveBackend)(nil)

func (b *serveBackend) serviceStore() (session.ServiceStore, error) {
	store, ok := b.store.(session.ServiceStore)
	if !ok {
		return nil, errors.New("session store does not expose service state")
	}
	return store, nil
}

func (b *serveBackend) groupStore() (session.GroupStore, error) {
	store, ok := b.store.(session.GroupStore)
	if !ok {
		return nil, errors.New("session store does not expose groups")
	}
	return store, nil
}

func (b *serveBackend) GroupByName(name string) (core.Group, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.groupStore()
	if err != nil {
		return core.Group{}, false, err
	}
	return store.GroupByName(name)
}

func (b *serveBackend) GroupBySession(sessionID string) (core.Group, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.groupStore()
	if err != nil {
		return core.Group{}, false, err
	}
	return store.GroupBySession(sessionID)
}

func (b *serveBackend) GroupMembers(groupID string) ([]core.GroupMember, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.groupStore()
	if err != nil {
		return nil, err
	}
	return store.GroupMembers(groupID)
}

func (b *serveBackend) Groups() ([]core.Group, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.groupStore()
	if err != nil {
		return nil, err
	}
	return store.Groups()
}

func (b *serveBackend) taskStore() (session.TaskStore, error) {
	store, ok := b.store.(session.TaskStore)
	if !ok {
		return nil, errors.New("session store does not expose a group board")
	}
	return store, nil
}

func (b *serveBackend) CreateTask(t core.Task) (core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.taskStore()
	if err != nil {
		return core.Task{}, err
	}
	return store.CreateTask(t)
}

func (b *serveBackend) Task(id string) (core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.taskStore()
	if err != nil {
		return core.Task{}, err
	}
	return store.Task(id)
}

func (b *serveBackend) Tasks(groupID string, statuses []core.TaskStatus, limit int) ([]core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.taskStore()
	if err != nil {
		return nil, err
	}
	return store.Tasks(groupID, statuses, limit)
}

func (b *serveBackend) ClaimTask(id, ownerID string, now time.Time, lease time.Duration) (core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.taskStore()
	if err != nil {
		return core.Task{}, err
	}
	return store.ClaimTask(id, ownerID, now, lease)
}

func (b *serveBackend) UpdateTask(id, actorID string, expectVersion int, status core.TaskStatus, note string, allowForeign bool, now time.Time) (core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.taskStore()
	if err != nil {
		return core.Task{}, err
	}
	return store.UpdateTask(id, actorID, expectVersion, status, note, allowForeign, now)
}

func (b *serveBackend) ExpireTasks(now time.Time) ([]core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.taskStore()
	if err != nil {
		return nil, err
	}
	return store.ExpireTasks(now)
}

func (b *serveBackend) OrphanTasksOwnedBy(sessionID string, now time.Time) ([]core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.taskStore()
	if err != nil {
		return nil, err
	}
	return store.OrphanTasksOwnedBy(sessionID, now)
}

func (b *serveBackend) DueEscalations(groupID string, now time.Time) ([]core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.taskStore()
	if err != nil {
		return nil, err
	}
	return store.DueEscalations(groupID, now)
}

func (b *serveBackend) MarkTaskEscalated(id string, kind core.TaskStatus, now time.Time) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.taskStore()
	if err != nil {
		return false, err
	}
	return store.MarkTaskEscalated(id, kind, now)
}

func (b *serveBackend) AppendEvent(sessionID string, e core.Event) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.serviceStore()
	if err != nil {
		return 0, err
	}
	e.Session = sessionID
	return store.AppendEvent(e)
}

func (b *serveBackend) AckEvents(_ string, ids []int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.serviceStore()
	if err != nil {
		return err
	}
	return store.AckEvents(ids)
}

func (b *serveBackend) NackEvent(_ string, id int64, cause error, retryAfter time.Duration, dead bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.serviceStore()
	if err != nil {
		return err
	}
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	return store.NackEvent(id, time.Now().Add(retryAfter), msg, dead)
}

func (b *serveBackend) DueEvents(sessionID string, now time.Time, limit int) ([]core.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.serviceStore()
	if err != nil {
		return nil, err
	}
	return store.DueEvents(sessionID, now, limit)
}

func (b *serveBackend) NextEventAt(sessionID string) (time.Time, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.serviceStore()
	if err != nil {
		return time.Time{}, false, err
	}
	return store.NextEventAt(sessionID)
}

func (b *serveBackend) SetNextWake(name string, at *time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.serviceStore()
	if err != nil {
		return err
	}
	return store.SetNextWake(name, at)
}

func (b *serveBackend) SetServiceState(name, state, status string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.serviceStore()
	if err != nil {
		return err
	}
	return store.SetServiceState(name, state, status)
}

func (b *serveBackend) ServiceSpec(name string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.serviceStore()
	if err != nil {
		return "", err
	}
	info, err := store.ServiceInfo(name)
	if err != nil {
		return "", err
	}
	return info.Spec, nil
}

func (b *serveBackend) ServiceView(name string) (runner.ServiceView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.serviceViewLocked(name)
}

// serviceViewLocked reads a service view with b.mu already held, so creation can
// return the row it just wrote without re-locking.
func (b *serveBackend) serviceViewLocked(name string) (runner.ServiceView, error) {
	store, err := b.serviceStore()
	if err != nil {
		return runner.ServiceView{}, err
	}
	info, err := store.ServiceInfo(name)
	if err != nil {
		return runner.ServiceView{}, fmt.Errorf("%w: %s", runner.ErrNotFound, name)
	}
	watchers, err := store.Watchers(info.ID)
	if err != nil {
		return runner.ServiceView{}, err
	}
	view := runner.ServiceView{Meta: serveMeta(info.Meta), Status: info.Status, Spec: info.Spec}
	if info.Spec != "" {
		if manifest, err := config.ParseStoredServiceSpec(info.Spec); err == nil {
			if text, err := config.RenderServiceManifestTOML(manifest); err == nil {
				view.ManifestTOML = text
			}
		}
	}
	for _, w := range watchers {
		view.Watchers = append(view.Watchers, runner.WatcherState{
			ID: w.ID, SessionID: w.SessionID, Name: w.Name, Spec: w.Spec,
			Enabled: w.Enabled, ApprovedAt: w.ApprovedAt,
			LastRun: w.LastRun, NextRun: w.NextRun, LastState: w.LastState, LastError: w.LastError,
		})
	}
	// Pending work is part of the service's visible state: an operator needs to
	// see mail or a probe event that has not been handled yet, not just a count.
	queued, err := store.QueuedEvents(info.ID, serviceQueueLimit)
	if err != nil {
		return runner.ServiceView{}, err
	}
	for _, ev := range queued {
		view.Queue = append(view.Queue, runner.QueuedEvent{
			ID: ev.ID, Class: ev.Class.String(), Kind: ev.Kind, Source: ev.Source,
			Payload: ev.Payload, At: ev.At, Deadline: ev.Deadline, Attempts: ev.Attempts, State: ev.State,
		})
	}
	return view, nil
}

// CreateService validates a manifest exactly like `pai service create`, then
// writes it through the shared newService helper. A pasted manifest may define
// command watchers: they are stored unapproved unless approveCommands is set,
// and a probe without a grant does not run.
func (b *serveBackend) CreateService(name, manifestText string, approveCommands bool) (runner.ServiceView, error) {
	if !session.ValidName(name) {
		return runner.ServiceView{}, fmt.Errorf("invalid session name %q (allowed: letters, digits, . _ -)", name)
	}
	manifest, err := config.ParseServiceManifest([]byte(manifestText))
	if err != nil {
		return runner.ServiceView{}, err
	}
	if err := prepareServiceManifest(manifest); err != nil {
		return runner.ServiceView{}, err
	}
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return runner.ServiceView{}, fmt.Errorf("load config: %w", err)
	}
	if cfg.DefaultModel == "" {
		return runner.ServiceView{}, errors.New("no default model is configured")
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.serviceStore(); err != nil {
		return runner.ServiceView{}, err
	}
	if _, err := b.store.Get(name); err == nil {
		return runner.ServiceView{}, fmt.Errorf("session %q already exists", name)
	} else if !errors.Is(err, session.ErrNotFound) {
		return runner.ServiceView{}, err
	}
	if _, _, err := newService(b.store, name, manifest, cfg.DefaultModel, time.Now(), approveCommands); err != nil {
		return runner.ServiceView{}, err
	}
	return b.serviceViewLocked(name)
}

// EditService replaces a stopped service's manifest through the shared CLI
// helper, so validation, grants, and watcher reconciliation cannot drift between
// the two entry points.
func (b *serveBackend) EditService(name, manifestText string, approveCommands bool) (runner.ServiceView, error) {
	if !session.ValidName(name) {
		return runner.ServiceView{}, fmt.Errorf("invalid session name %q (allowed: letters, digits, . _ -)", name)
	}
	manifest, err := config.ParseServiceManifest([]byte(manifestText))
	if err != nil {
		return runner.ServiceView{}, err
	}
	if err := prepareServiceManifest(manifest); err != nil {
		return runner.ServiceView{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.serviceStore(); err != nil {
		return runner.ServiceView{}, err
	}
	if _, err := b.store.Get(name); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return runner.ServiceView{}, fmt.Errorf("%w: %s", runner.ErrNotFound, name)
		}
		return runner.ServiceView{}, err
	}
	if err := updateService(b.store, name, manifest, time.Now(), approveCommands); err != nil {
		return runner.ServiceView{}, err
	}
	return b.serviceViewLocked(name)
}

func (b *serveBackend) Watchers(sessionID string) ([]runner.WatcherState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.serviceStore()
	if err != nil {
		return nil, err
	}
	stored, err := store.Watchers(sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]runner.WatcherState, 0, len(stored))
	for _, w := range stored {
		out = append(out, runner.WatcherState{
			ID: w.ID, SessionID: w.SessionID, Name: w.Name, Spec: w.Spec,
			Enabled: w.Enabled, ApprovedAt: w.ApprovedAt,
			LastRun: w.LastRun, NextRun: w.NextRun, LastState: w.LastState, LastError: w.LastError,
		})
	}
	return out, nil
}

func (b *serveBackend) PutWatcher(w runner.WatcherState) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.serviceStore()
	if err != nil {
		return err
	}
	return store.PutWatcher(session.Watcher{
		ID: w.ID, SessionID: w.SessionID, Name: w.Name, Spec: w.Spec,
		Enabled: w.Enabled, ApprovedAt: w.ApprovedAt,
		LastRun: w.LastRun, NextRun: w.NextRun, LastState: w.LastState, LastError: w.LastError,
	})
}

func (b *serveBackend) SetWatcherState(id string, lastRun, nextRun *time.Time, lastState, lastErr string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	store, err := b.serviceStore()
	if err != nil {
		return err
	}
	return store.SetWatcherState(id, lastRun, nextRun, lastState, lastErr)
}

func (b *serveBackend) List() ([]runner.Meta, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	metas, err := b.store.List()
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	out := make([]runner.Meta, 0, len(metas))
	for _, m := range metas {
		out = append(out, serveMeta(m))
	}
	return out, nil
}

func (b *serveBackend) History(name string, offset, limit int) (runner.History, error) {
	if !session.ValidName(name) {
		return runner.History{}, errors.New("invalid session name")
	}
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 200 {
		return runner.History{}, errors.New("invalid history pagination")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s, err := b.store.Get(name)
	if errors.Is(err, session.ErrNotFound) {
		return runner.History{}, runner.ErrNotFound
	}
	if err != nil {
		return runner.History{}, fmt.Errorf("get history: %w", err)
	}
	start := min(offset, len(s.Turns))
	end := min(start+limit, len(s.Turns))
	turns := append([]core.Turn{}, s.Turns[start:end]...)
	return runner.History{Meta: serveMeta(s.Meta), Turns: turns, Total: len(s.Turns)}, nil
}

// Context reports a window of the messages a session would send on its next
// request: the stored turns projected through the same replay, override and
// elision the loop applies per request, plus the role's head and tail. offset and
// limit window the cards (their Seq is dense and 1-based, so offset is the index
// of the first card), and the whole projection is still computed over every turn
// — elision and the replay window depend on the full transcript, not the window.
// It reads only persisted state, so it never needs a live runtime.
func (b *serveBackend) Context(name string, offset, limit int) (runner.ContextView, error) {
	if !session.ValidName(name) {
		return runner.ContextView{}, errors.New("invalid session name")
	}
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 200 {
		return runner.ContextView{}, errors.New("invalid context pagination")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s, err := b.store.Get(name)
	if errors.Is(err, session.ErrNotFound) {
		return runner.ContextView{}, runner.ErrNotFound
	}
	if err != nil {
		return runner.ContextView{}, fmt.Errorf("get session: %w", err)
	}
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return runner.ContextView{}, fmt.Errorf("load config: %w", err)
	}
	role := s.Meta.Role
	if role == "" {
		role = cfg.DefaultRole
	}
	extra, extraTools, err := config.LoadRoleExtraEntry(role)
	if err != nil {
		return runner.ContextView{}, fmt.Errorf("load role extra: %w", err)
	}
	rp, err := chat.LoadRolePromptForSession(role, extra, extraTools, cfg.TrustedProjects, s.Meta.Cwd, s.Meta.CreatedAt)
	if err != nil {
		return runner.ContextView{}, fmt.Errorf("load role: %w", err)
	}
	// The same trigger conversion the loop applies, so what Inspect shows is what a
	// request would send when the estimate has been calibrated against this session.
	scale := s.Meta.TokenScale()
	servedCfg := chat.ScaledContext(cfg.Context, scale)
	cards := chat.ContextCards(s.Turns, toChatOverrides(s.Overrides), servedCfg, cfg.SessionMaxTurns)
	start := min(offset, len(cards))
	end := min(start+limit, len(cards))
	window := cards[start:end]
	// Size the prompt the loop would compose on resume, after the same replay-time
	// edits it applies (over-run cut, elision), so the number a client shows is the one
	// that would actually be sent and the one the summarization budget starts from.
	served := chat.ApplyOverrides(chat.ReplayTurns(s.Turns, cfg.Context.KeepTurns, cfg.SessionMaxTurns), toChatOverrides(s.Overrides))
	servedMsgs := chat.ServedHistory(toMessages(served), servedCfg)
	// What a compaction would absorb, in the same terms the loop decides with, so
	// the client can say why a manual compress has nothing to do.
	span, _ := chat.SplitForSummary(servedMsgs, cfg.Context.KeepTurns)
	view := runner.ContextView{
		Head:            rp.Head(),
		Tail:            rp.Tail(),
		Model:           s.Meta.Model,
		Total:           len(cards),
		Offset:          start,
		EstimatedTokens: chat.ScaleTokens(chat.EstimateTokens(rp.Messages(servedMsgs)), scale),
		EstimateScale:   scale,
		CompactTurns:    chat.UserTurns(span),
		UserTurns:       chat.UserTurns(servedMsgs),
		Cards:           make([]runner.ContextCard, len(window)),
		Config: runner.ContextSettings{
			KeepTurns:        cfg.Context.KeepTurns,
			ElideAfterTokens: cfg.Context.ElideLimit(),
			ElideMinBytes:    cfg.Context.ElideMinBytes,
			ElideHeadLines:   cfg.Context.ElideHeadLines,
			ElideTailLines:   cfg.Context.ElideTailLines,
			MaxTurns:         cfg.SessionMaxTurns,
		},
	}
	for i, c := range window {
		view.Cards[i] = runner.ContextCard{Seq: c.Seq, Role: c.Role, Kind: c.Kind, Original: c.Original, Sent: c.Sent, State: c.State, Essential: c.Essential}
	}
	return view, nil
}

// SetOverride records the user's surgery on one turn. It changes only what a run
// sends, never the stored turn; the manager has already retired the idle runtime
// so the next instruction re-prepares with it.
func (b *serveBackend) SetOverride(name string, seq int, action, content string) error {
	if !session.ValidName(name) {
		return errors.New("invalid session name")
	}
	if seq < 1 {
		return errors.New("turn sequence must be positive")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	err := b.store.SetOverride(name, session.Override{Seq: seq, Action: action, Content: content})
	return overrideError(err)
}

// DeleteOverride clears the surgery on one turn, restoring its stored text.
func (b *serveBackend) DeleteOverride(name string, seq int) error {
	if !session.ValidName(name) {
		return errors.New("invalid session name")
	}
	if seq < 1 {
		return errors.New("turn sequence must be positive")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return overrideError(b.store.DeleteOverride(name, seq))
}

// SetEssential marks or clears the user's "keep this turn" flag on one stored
// turn. Like an override it changes only what a run sends, never the stored text.
func (b *serveBackend) SetEssential(name string, seq int, on bool) error {
	if !session.ValidName(name) {
		return errors.New("invalid session name")
	}
	if seq < 1 {
		return errors.New("turn sequence must be positive")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return overrideError(b.store.SetEssential(name, seq, on))
}

// overrideError translates the store's missing-session/turn signal into the
// runner's, so the manager and HTTP layer share one meaning for "not found".
func overrideError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, session.ErrNotFound) {
		return runner.ErrNotFound
	}
	return fmt.Errorf("context override: %w", err)
}

func (b *serveBackend) Prepare(name string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), error) {
	cfg, p, rec, history, cleanup, _, err := b.PrepareWorkspace(name, "")
	return cfg, p, rec, history, cleanup, err
}

func (b *serveBackend) Models() ([]string, string, error) {
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load config: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	metas, err := b.store.List()
	if err != nil {
		return nil, "", fmt.Errorf("list session models: %w", err)
	}
	seen := map[string]bool{cfg.DefaultModel: true}
	for _, model := range cfg.PresetModels {
		if model != "" {
			seen[model] = true
		}
	}
	for _, meta := range metas {
		if meta.Model != "" {
			seen[meta.Model] = true
		}
	}
	models := make([]string, 0, len(seen))
	for model := range seen {
		models = append(models, model)
	}
	sort.Strings(models)
	return models, cfg.DefaultModel, nil
}

func (b *serveBackend) SetModel(name, model string) error {
	if !session.ValidName(name) {
		return errors.New("invalid session name")
	}
	providerName, modelName, found := strings.Cut(model, ":")
	providerName, modelName = strings.TrimSpace(providerName), strings.TrimSpace(modelName)
	model = providerName + ":" + modelName
	if !found || providerName == "" || strings.TrimSpace(modelName) == "" || strings.IndexFunc(model, unicode.IsSpace) >= 0 || strings.IndexFunc(model, unicode.IsControl) >= 0 {
		return errors.New("model must be provider:model with nonempty names and no whitespace")
	}
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	pc, ok := cfg.ProvidersConfigs[providerName]
	if !ok {
		return fmt.Errorf("provider %q is not configured", providerName)
	}
	if _, err := provider.CreateClient(provider.ProviderConfig{
		Name:     providerName,
		APIKey:   pc.APIKey,
		Model:    modelName,
		BaseURL:  pc.BaseURL,
		Protocol: pc.Protocol,
		Headers:  pc.Headers,
	}); err != nil {
		return fmt.Errorf("validate provider: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.store.SetModel(name, model); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return runner.ErrNotFound
		}
		return fmt.Errorf("set session model: %w", err)
	}
	return nil
}

// Rewind drops every turn from index keep onward. When backup is true (or nil
// and [session] rewind_backup is on), the current turns are first copied into a
// rolling backup session so the dropped tail stays attachable.
func (b *serveBackend) Rewind(name string, keep int, backup *bool) error {
	if !session.ValidName(name) {
		return errors.New("invalid session name")
	}
	if keep < 0 {
		return errors.New("keep must be non-negative")
	}
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	doBackup := cfg.SessionRewindBackup
	if backup != nil {
		doBackup = *backup
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s, err := b.store.Get(name)
	if errors.Is(err, session.ErrNotFound) {
		return runner.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("get session: %w", err)
	}
	if keep >= len(s.Turns) {
		return nil // nothing after the rewind point
	}
	if doBackup {
		if err := b.writeBackup(*s); err != nil {
			return fmt.Errorf("rewind backup: %w", err)
		}
	}
	if err := b.store.Truncate(name, keep); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return runner.ErrNotFound
		}
		return fmt.Errorf("truncate session: %w", err)
	}
	return nil
}

// writeBackup replaces a session's rolling backup with its current turns. At
// most one backup per conversation exists; each rewind overwrites it, so the
// sidebar does not grow without bound.
func (b *serveBackend) writeBackup(s session.Session) error {
	bak := backupName(s.Meta.Name)
	if _, err := b.store.Get(bak); err == nil {
		if err := b.store.Delete(bak); err != nil {
			return err
		}
	} else if !errors.Is(err, session.ErrNotFound) {
		return err
	}
	meta := s.Meta
	meta.Name = bak
	meta.Title = "backup of " + s.Meta.Name
	if _, err := b.store.Create(meta); err != nil {
		return err
	}
	if len(s.Turns) > 0 {
		return b.store.Append(bak, s.Turns...)
	}
	return nil
}

// backupName derives the rolling backup name, kept within the valid-name length.
func backupName(name string) string {
	const suffix = ".rewind-backup"
	if len(name)+len(suffix) <= 64 {
		return name + suffix
	}
	return name[:64-len(suffix)] + suffix
}

func (b *serveBackend) Roles() ([]string, string, error) {
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load config: %w", err)
	}
	return prompts.RoleNames(), cfg.DefaultRole, nil
}

// SetRole persists a new role for a session. The next instruction re-prepares
// with the new role's prompt and tools.
func (b *serveBackend) SetRole(name, role string) error {
	if !session.ValidName(name) {
		return errors.New("invalid session name")
	}
	valid := false
	for _, candidate := range prompts.RoleNames() {
		if candidate == role {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("unknown role %q (available: %s)", role, strings.Join(prompts.RoleNames(), ", "))
	}
	if _, err := chat.LoadRolePrompt(role, ""); err != nil {
		return fmt.Errorf("load role: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	sess, err := b.store.Get(name)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return runner.ErrNotFound
		}
		return fmt.Errorf("get session: %w", err)
	}
	if err := b.store.SetRole(name, role); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return runner.ErrNotFound
		}
		return fmt.Errorf("set session role: %w", err)
	}
	// A service's manifest is the durable definition the view and a restart read,
	// so its role must agree with the session metadata.
	if sess.Meta.Mode == session.ModeService {
		if ss, ok := b.store.(session.ServiceStore); ok {
			if info, err := ss.ServiceInfo(name); err == nil && info.Spec != "" {
				if manifest, err := config.ParseStoredServiceSpec(info.Spec); err == nil {
					manifest.Role = role
					if spec, err := config.MarshalServiceManifest(manifest); err == nil {
						if err := ss.SetServiceSpec(name, spec); err != nil {
							return fmt.Errorf("update service role: %w", err)
						}
					}
				}
			}
		}
	}
	return nil
}

// SetWorkingDir changes the workspace used by the next instruction without
// changing process cwd or the session's conversation.
func (b *serveBackend) SetWorkingDir(name, cwd string) error {
	if !session.ValidName(name) {
		return errors.New("invalid session name")
	}
	if strings.TrimSpace(cwd) == "" {
		return errors.New("working directory must not be blank")
	}
	dir, err := runner.ResolveWorkingDir(cwd)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.store.SetWorkingDir(name, dir); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return runner.ErrNotFound
		}
		return fmt.Errorf("set session working directory: %w", err)
	}
	return nil
}

// Rename changes a session's name. The next instruction starts a worker under
// the new name.
func (b *serveBackend) Rename(name, newName string) error {
	if !session.ValidName(name) || !session.ValidName(newName) {
		return errors.New("invalid session name")
	}
	if name == newName {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.store.Rename(name, newName); err != nil {
		switch {
		case errors.Is(err, session.ErrNotFound):
			return runner.ErrNotFound
		case errors.Is(err, session.ErrExists):
			return errors.New("a session with that name already exists")
		}
		return fmt.Errorf("rename session: %w", err)
	}
	return nil
}

// Delete removes a session and its stored history. The manager retires the idle
// runtime before this runs, so nothing is left serving a deleted session.
func (b *serveBackend) Delete(name string) error {
	if !session.ValidName(name) {
		return errors.New("invalid session name")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.store.Delete(name); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return runner.ErrNotFound
		}
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (b *serveBackend) Create(name, cwd string) (runner.Meta, error) {
	return b.CreateWithRole(name, cwd, "")
}

func (b *serveBackend) CreateWithRole(name, cwd, role string) (runner.Meta, error) {
	if !session.ValidName(name) {
		return runner.Meta{}, errors.New("invalid session name")
	}
	if cwd == "" {
		cwd = b.cwd
	}
	dir, err := runner.ResolveWorkingDir(cwd)
	if err != nil {
		return runner.Meta{}, err
	}
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return runner.Meta{}, fmt.Errorf("load config: %w", err)
	}
	if role == "" {
		role = cfg.DefaultRole
	}
	valid := false
	for _, name := range prompts.RoleNames() {
		if name == role {
			valid = true
			break
		}
	}
	if !valid {
		return runner.Meta{}, fmt.Errorf("unknown role %q (available: %s)", role, strings.Join(prompts.RoleNames(), ", "))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s, err := b.store.Create(session.Meta{Name: name, Role: role, Model: cfg.DefaultModel, Cwd: dir})
	if err != nil {
		return runner.Meta{}, fmt.Errorf("create session: %w", err)
	}
	return serveMeta(s.Meta), nil
}

func (b *serveBackend) PrepareWorkspace(name, cwd string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), runner.Meta, error) {
	fail := func(err error) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), runner.Meta, error) {
		return nil, nil, nil, nil, nil, runner.Meta{}, err
	}
	if !session.ValidName(name) {
		return fail(errors.New("invalid session name"))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s, err := b.store.Get(name)
	if err != nil && !errors.Is(err, session.ErrNotFound) {
		return fail(fmt.Errorf("get session: %w", err))
	}
	exists := err == nil
	if exists {
		if cwd != "" {
			requested, err := runner.ResolveWorkingDir(cwd)
			if err != nil {
				return fail(err)
			}
			stored, err := runner.ResolveWorkingDir(s.Meta.Cwd)
			if err != nil {
				return fail(err)
			}
			if requested != stored {
				return fail(errors.New("requested working directory differs from session workspace"))
			}
		}
		cwd = s.Meta.Cwd
	} else if cwd == "" {
		cwd = b.cwd
	}
	cwd, err = runner.ResolveWorkingDir(cwd)
	if err != nil {
		return fail(err)
	}
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return fail(fmt.Errorf("load config: %w", err))
	}
	if exists {
		cfg.DefaultRole = s.Meta.Role
		cfg.SetModel(s.Meta.Model)
	}
	if cfg.Provider == "" || cfg.Model == "" {
		return fail(errors.New("model must be provider:model"))
	}
	extra, extraTools, err := config.LoadRoleExtraEntry(cfg.DefaultRole)
	if err != nil {
		return fail(fmt.Errorf("load role extra: %w", err))
	}
	cfg.RoleExtra = extra
	cfg.RoleExtraTools = extraTools
	if _, err := chat.LoadRolePromptForSession(cfg.DefaultRole, extra, extraTools, cfg.TrustedProjects, cwd, time.Time{}); err != nil {
		return fail(fmt.Errorf("load role: %w", err))
	}
	pc := cfg.ProvidersConfigs[cfg.Provider]
	client, err := provider.CreateClient(provider.ProviderConfig{
		Name:     cfg.Provider,
		APIKey:   pc.APIKey,
		Model:    cfg.Model,
		BaseURL:  pc.BaseURL,
		Protocol: pc.Protocol,
		Headers:  pc.Headers,
	})
	if err != nil {
		return fail(fmt.Errorf("create provider: %w", err))
	}
	if !exists {
		s, err = b.store.Create(session.Meta{Name: name, Role: cfg.DefaultRole, Model: cfg.DefaultModel, Cwd: cwd})
		if err != nil {
			return fail(fmt.Errorf("create session: %w", err))
		}
	}
	served := chat.ApplyOverrides(chat.ReplayTurns(s.Turns, cfg.Context.KeepTurns, cfg.SessionMaxTurns), toChatOverrides(s.Overrides))
	return cfg, client, &serveRecorder{backend: b, name: name}, toMessages(served), func() {}, serveMeta(s.Meta), nil
}

type serveRecorder struct {
	backend *serveBackend
	name    string
}

func (r *serveRecorder) AppendTurn(t core.Turn) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if err := r.backend.store.Append(r.name, t); err != nil {
		return fmt.Errorf("append session turn: %w", err)
	}
	return nil
}

func (r *serveRecorder) HasTurnEvent(eventID int64) (bool, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	return r.backend.store.HasTurnEvent(r.name, eventID)
}

// SetUsage persists the token accounting the estimate is calibrated from.
func (r *serveRecorder) SetUsage(reported, estimate int) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if err := r.backend.store.SetUsage(r.name, reported, estimate); err != nil {
		return fmt.Errorf("record session usage: %w", err)
	}
	return nil
}

// ReasoningDefault reports the configured [app] reasoning effort, the default for
// sessions that carry no in-memory override.
func (b *serveBackend) ReasoningDefault() (string, error) {
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return "", err
	}
	if cfg.ReasoningEffort == "" {
		return string(provider.ReasoningEffortNone), nil
	}
	return string(cfg.ReasoningEffort), nil
}

var _ runner.Backend = (*serveBackend)(nil)
var _ runner.ContextBackend = (*serveBackend)(nil)
var _ runner.ContextSetter = (*serveBackend)(nil)
var _ runner.ModelBackend = (*serveBackend)(nil)
var _ runner.WorkingDirSetter = (*serveBackend)(nil)
var _ runner.ReasoningBackend = (*serveBackend)(nil)
