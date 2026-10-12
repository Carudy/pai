package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
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

func serveMeta(m session.Meta) runner.Meta {
	return runner.Meta{Name: m.Name, Role: m.Role, Model: m.Model, Cwd: m.Cwd, UpdatedAt: m.UpdatedAt}
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
	if _, err := chat.LoadRolePrompt(role, chat.IntroOverride{}); err != nil {
		return fmt.Errorf("load role: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.store.SetRole(name, role); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return runner.ErrNotFound
		}
		return fmt.Errorf("set session role: %w", err)
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

func (b *serveBackend) PrepareWorkspace(name, cwd string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), string, error) {
	fail := func(err error) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), string, error) {
		return nil, nil, nil, nil, nil, "", err
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
	custom, err := config.LoadCustomPrompt(cfg.DefaultRole)
	if err != nil {
		return fail(fmt.Errorf("load custom prompt: %w", err))
	}
	cfg.CustomPrompt = custom
	if _, err := chat.LoadRolePromptAt(cfg.DefaultRole, chat.IntroOverride{Prompt: custom.Prompt, Additional: custom.Additional}, cwd); err != nil {
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
	return cfg, client, &serveRecorder{backend: b, name: name}, toMessages(replayTurns(s.Turns, cfg.SessionMaxTurns)), func() {}, cwd, nil
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

var _ runner.Backend = (*serveBackend)(nil)
var _ runner.ModelBackend = (*serveBackend)(nil)
var _ runner.WorkingDirSetter = (*serveBackend)(nil)
