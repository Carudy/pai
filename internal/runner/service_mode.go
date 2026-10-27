package runner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
	"github.com/Carudy/pai/internal/role"
)

type serviceResult struct {
	err error
}

// StartServices registers every persisted service with the scheduler. Chat
// sessions are untouched. It is called once by the composition root after the
// manager is built.
func (m *Manager) StartServices() error {
	metas, err := m.backend.List()
	if err != nil {
		return err
	}
	specs, _ := m.backend.(ServiceInfoBackend)
	for _, meta := range metas {
		if meta.Mode != ModeService {
			continue
		}
		spec := ""
		if specs != nil {
			spec, _ = specs.ServiceSpec(meta.Name)
		}
		m.mu.Lock()
		if m.closed || m.ctx.Err() != nil {
			m.mu.Unlock()
			return ErrClosed
		}
		m.serviceNames[meta.Name] = true
		m.sessionIDs[meta.Name] = meta.ID
		if spec != "" {
			m.serviceSpecs[meta.Name] = spec
		}
		// A stopped service is loaded but not scheduled: stopping is durable, so a
		// restart must not silently resume it.
		stopped := meta.State == StateStopped
		if stopped {
			m.stoppedServices[meta.Name] = true
		} else {
			delete(m.stoppedServices, meta.Name)
		}
		m.mu.Unlock()
		if !stopped {
			m.scheduler.Register(meta.Name, meta.NextWake)
		}
	}
	return nil
}

// RegisterService schedules a service by name without preparing its runtime.
// The worker is created lazily on the first wake.
func (m *Manager) RegisterService(name string, next *time.Time) {
	m.scheduler.Register(name, next)
}

// WakeService delivers an already-built wake to a registered service.
func (m *Manager) WakeService(name string, wake core.Wake) bool {
	return m.scheduler.Wake(name, wake)
}

// Activate implements ServiceHost: it prepares or reuses the service's resident
// worker, runs any due probes, and — only when something warrants it — hands one
// wake to RunService and waits for that activation to end.
func (m *Manager) Activate(ctx context.Context, name string, wake core.Wake) (*time.Time, error) {
	w, err := m.serviceWorker(ctx, name)
	if err != nil {
		if errors.Is(err, ErrStopped) {
			return nil, err
		}
		// A preparation failure (including a settings change that is retiring the
		// worker) must not silently unschedule the service: ask the scheduler to
		// look again shortly.
		retry := time.Now().Add(30 * time.Second)
		return &retry, err
	}
	now := time.Now()
	m.mu.Lock()
	w.lastUsed = now
	m.mu.Unlock()
	m.enforceResidentLimit(name)

	// Recover durable work first, so a user instruction is seen before the probe
	// pass. A person waiting for an answer must not sit behind up to 15 seconds of
	// probes; the due watchers stay due and run on the next activation instead.
	wake.Events = mergeEvents(wake.Events, m.dueEvents(w, now))

	var probeEvents []core.Event
	var probeNext *time.Time
	if hasUserInput(wake.Events) {
		probeNext = m.nextWatcherRun(w, now)
	} else {
		probeEvents, probeNext = m.probeDue(ctx, w, now)
		wake.Events = mergeEvents(wake.Events, probeEvents)
	}

	// A bare clock tick is only the scheduler's hint to probe. When no probe
	// crossed its condition, the activation ends here: an unchanged world costs no
	// model call.
	if !hasSubstantive(wake.Events) {
		next := m.nextServiceWake(name, probeNext, time.Now())
		m.persistNextWake(w, name, next)
		return next, nil
	}

	if strings.TrimSpace(wake.Situation) == "" {
		wake.Situation = renderSituation(wake)
	}
	_, err = w.activate(ctx, wake)
	if err != nil {
		// The wake's events were nacked with a backoff; make sure the scheduler
		// looks again even if no watcher would have woken the service.
		retry := time.Now().Add(30 * time.Second)
		probeNext = earlier(probeNext, &retry)
	}
	at := m.nextServiceWake(name, probeNext, time.Now())
	m.persistNextWake(w, name, at)
	return at, err
}

// persistNextWake records the timer the scheduler will use after a restart. A
// failure is surfaced but does not abort the run: the in-memory scheduler still
// knows when to wake.
func (m *Manager) persistNextWake(w *worker, name string, at *time.Time) {
	eb, ok := m.backend.(ServiceEventBackend)
	if !ok {
		return
	}
	if err := eb.SetNextWake(name, at); err != nil {
		w.emit("error", "could not persist the next wake: "+err.Error())
	}
}

func (m *Manager) isService(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.serviceNames[name] {
		return true
	}
	w := m.entries[name]
	return w != nil && w.mode == ModeService
}

// sendServiceEvent turns a user instruction into a durable EventUser and wakes
// the scheduler, instead of pushing to a chat worker's task channel.
func (m *Manager) sendServiceEvent(name, text string) error {
	if err := validate(text); err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed || m.ctx.Err() != nil {
		m.mu.Unlock()
		return ErrClosed
	}
	id := m.sessionIDs[name]
	known := m.serviceNames[name]
	stopped := m.stoppedServices[name]
	m.mu.Unlock()
	if !known {
		return ErrNotFound
	}
	if stopped {
		return ErrStopped
	}

	ev := core.Event{
		Session: id, Class: core.EventUser, Kind: "user_input", Source: "user",
		Payload: text, At: time.Now(),
	}
	if eb, ok := m.backend.(ServiceEventBackend); ok && id != "" {
		eid, err := eb.AppendEvent(id, ev)
		if err != nil {
			return fmt.Errorf("append user event: %w", err)
		}
		ev.ID = eid
	}
	wake := core.Wake{Reason: "user input", Events: []core.Event{ev}}
	if m.scheduler.Wake(name, wake) {
		return nil
	}
	// The session may be known but not yet registered (for example a service
	// created after startup). Register it and retry once — but never resurrect a
	// service that was stopped while this send was in flight.
	m.mu.Lock()
	stopped = m.stoppedServices[name]
	m.mu.Unlock()
	if stopped {
		return ErrStopped
	}
	m.scheduler.Register(name, nil)
	if m.scheduler.Wake(name, wake) {
		return nil
	}
	return ErrNotFound
}

// CreateService delegates creation to the backend and registers the result with
// the scheduler, so a service added through the UI starts running without a
// server restart.
func (m *Manager) CreateService(name, manifest string, approveCommands bool) (ServiceView, error) {
	backend, ok := m.backend.(ServiceCreator)
	if !ok {
		return ServiceView{}, errors.New("this backend cannot create services")
	}
	view, err := backend.CreateService(name, manifest, approveCommands)
	if err != nil {
		return ServiceView{}, err
	}
	m.mu.Lock()
	m.serviceNames[view.Name] = true
	m.sessionIDs[view.Name] = view.ID
	m.serviceSpecs[view.Name] = view.Spec
	delete(m.stoppedServices, view.Name)
	m.mu.Unlock()
	m.scheduler.Register(view.Name, view.NextWake)
	return view, nil
}

// EditService replaces a stopped service's manifest. The backend owns the store
// write; the manager only refreshes its cached spec and identity so the next
// start reads the new definition. The scheduler is not touched: the service is
// stopped by construction.
func (m *Manager) EditService(name, manifest string, approveCommands bool) (ServiceView, error) {
	backend, ok := m.backend.(ServiceEditor)
	if !ok {
		return ServiceView{}, errors.New("this backend cannot edit services")
	}
	view, err := backend.EditService(name, manifest, approveCommands)
	if err != nil {
		return ServiceView{}, err
	}
	m.mu.Lock()
	m.serviceNames[view.Name] = true
	m.sessionIDs[view.Name] = view.ID
	m.serviceSpecs[view.Name] = view.Spec
	m.stoppedServices[view.Name] = true
	m.mu.Unlock()
	return view, nil
}

// ServiceView reports one resident session: durable state from the store, plus
// the live phase and last error when the worker is prepared. It is the read side
// of `pai service show`, available while a server holds the store lock.
func (m *Manager) ServiceView(name string) (ServiceView, error) {
	backend, ok := m.backend.(ServiceBackend)
	if !ok {
		return ServiceView{}, errors.New("this backend does not expose service views")
	}
	view, err := backend.ServiceView(name)
	if err != nil {
		return ServiceView{}, err
	}
	if view.Mode != ModeService {
		return ServiceView{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	m.mu.Lock()
	if w := m.entries[name]; w != nil {
		if w.phase != "" {
			view.Phase = w.phase
		}
		if w.state != "" {
			view.State = w.state
		}
		view.Error = w.failure
	}
	m.mu.Unlock()
	return view, nil
}

// SetMaxResident bounds how many service runtimes stay prepared. Zero means
// unbounded. The bound is applied on the next activation, which retires the
// least recently used idle worker: its state and transcript are persisted, so
// the next wake re-prepares it.
func (m *Manager) SetMaxResident(n int) {
	m.mu.Lock()
	m.maxResident = n
	m.mu.Unlock()
}

// enforceResidentLimit retires idle service workers beyond the bound. keep is
// the worker the current activation is using, so it is never a victim. A busy,
// awaiting, or starting worker is skipped: eviction only ever cancels a worker
// that is blocked in the inbox.
func (m *Manager) enforceResidentLimit(keep string) {
	m.mu.Lock()
	limit := m.maxResident
	if limit <= 0 {
		m.mu.Unlock()
		return
	}
	residents := 0
	var idle []*worker
	for name, w := range m.entries {
		if w.mode != ModeService {
			continue
		}
		residents++
		if name == keep || w.state == StateBusy || w.state == StateAwaiting || w.state == StateStarting {
			continue
		}
		idle = append(idle, w)
	}
	excess := residents - limit
	m.mu.Unlock()
	if excess <= 0 {
		return
	}
	sort.Slice(idle, func(i, j int) bool { return idle[i].lastUsed.Before(idle[j].lastUsed) })
	for i := 0; i < excess && i < len(idle); i++ {
		idle[i].cancel()
	}
}

// serviceWorker returns the resident worker for a service, starting it on first
// use. A failed preparation is reported through readyErr.
func (m *Manager) serviceWorker(ctx context.Context, name string) (*worker, error) {
	m.mu.Lock()
	if m.closed || m.ctx.Err() != nil {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if m.stoppedServices[name] {
		m.mu.Unlock()
		return nil, ErrStopped
	}
	if m.changing[name] {
		// A settings change is retiring the worker: do not prepare a new one
		// against the metadata that is about to be replaced.
		m.mu.Unlock()
		return nil, ErrBusy
	}
	w := m.entries[name]
	if w == nil {
		wctx, cancel := context.WithCancel(m.ctx)
		w = &worker{
			m: m, name: name, mode: ModeService, ctx: wctx, cancel: cancel,
			done: make(chan struct{}), ready: make(chan struct{}),
			activation: make(chan serviceResult, 1),
			state:      StateStarting, phase: "starting",
			tasks: make(chan string, 1), steering: make(chan string, 1),
			subs: make(map[chan Event]struct{}),
		}
		m.entries[name] = w
		m.wg.Add(1)
		go w.runService()
		m.mu.Unlock()
	} else {
		if w.mode != ModeService {
			m.mu.Unlock()
			return nil, fmt.Errorf("session %q is not a service", name)
		}
		m.mu.Unlock()
	}
	select {
	case <-w.ready:
	case <-w.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if w.readyErr != nil {
		return nil, w.readyErr
	}
	return w, nil
}

// runService prepares the runtime once and then drives role.RunService across
// wakes. The worker stays resident (suspended) between activations.
func (w *worker) runService() {
	defer w.m.wg.Done()
	defer w.finishRun()

	var (
		copyCfg config.UserConfig
		rt      *role.Runtime
		rp      *chat.RolePrompt
	)
	cfg, p, rec, history, cleanup, meta, cwd, err := w.prepare()
	if cleanup != nil {
		defer cleanup()
	}
	if w.ctx.Err() != nil && err == nil {
		err = w.ctx.Err()
	}
	if err == nil {
		copyCfg = *cfg
		if v := w.reasoningOverride(); v != "" {
			copyCfg.ReasoningEffort = provider.ReasoningEffort(v)
		}
		rt = &role.Runtime{
			WorkingDir: cwd, SessionBirth: meta.CreatedAt, TokenScale: meta.TokenScale,
			Provider: p, Recorder: rec, Observer: observer{w}, Prompter: w, Logger: w,
			Interactive: false, SessionName: w.name,
		}
		rp, err = chat.LoadRolePromptForSession(copyCfg.DefaultRole, copyCfg.RoleExtra, copyCfg.RoleExtraTools, copyCfg.TrustedProjects, cwd, meta.CreatedAt)
		if err == nil {
			// The manifest's goal is the service's standing instruction. It belongs
			// in the head, so it is present on every wake and survives compaction.
			if manifest := w.m.serviceManifest(w.name); manifest != nil {
				rp.SetStanding(manifest.Goal)
			}
			w.m.mu.Lock()
			w.rt = rt
			rt.TrustedCmds = w.m.commandTrustLocked(w.name)
			rt.TrustedTools = w.m.toolTrustLocked(w.name)
			w.model = copyCfg.DefaultModel
			w.workingDir = cwd
			w.contextTokens = copyCfg.Context.SummarizeAfterTokens
			w.sessionID = meta.ID
			w.m.serviceNames[w.name] = true
			w.m.sessionIDs[w.name] = meta.ID
			w.inbox = NewServiceInbox(1, w.serviceDone)
			w.state = StateSuspended
			w.phase = "suspended"
			w.m.mu.Unlock()
		}
	}

	w.m.mu.Lock()
	w.readyErr = err
	w.m.mu.Unlock()
	close(w.ready)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			w.m.mu.Lock()
			w.failure = err.Error()
			w.emitLocked("error", err.Error())
			w.m.mu.Unlock()
		}
		return
	}

	err = role.RunService(w.ctx, &copyCfg, rt, rp, w.inbox, history)
	if err != nil && !errors.Is(err, context.Canceled) {
		w.m.mu.Lock()
		w.failure = err.Error()
		w.emitLocked("error", err.Error())
		w.m.mu.Unlock()
	}
}

// activate marks the worker busy, hands one wake to RunService, and waits for
// that activation to end.
func (w *worker) activate(ctx context.Context, wake core.Wake) (*time.Time, error) {
	w.m.mu.Lock()
	if w.state == StateStopped || w.inbox == nil {
		w.m.mu.Unlock()
		return nil, ErrNotFound
	}
	w.state = StateBusy
	w.phase = "waiting_model"
	w.emitLocked(StateBusy, nil)
	inbox := w.inbox
	w.m.mu.Unlock()

	if !inbox.Push(wake) {
		return nil, errors.New("service inbox already has a pending wake")
	}
	select {
	case result := <-w.activation:
		return nil, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-w.ctx.Done():
		return nil, w.ctx.Err()
	}
}

// serviceDone acknowledges the wake's events, records how the activation ended,
// and returns the worker to suspended or parked.
func (w *worker) serviceDone(wake core.Wake, res core.WakeResult) error {
	ackErr := w.ackWake(wake, res.Err)

	state, phase, status := StateSuspended, "suspended", res.Status
	switch res.Outcome {
	case "ask":
		state, phase = StateParked, "parked"
		if reason := strings.TrimSpace(res.Status); reason != "" {
			status = "waiting for an answer: " + reason
		} else {
			status = "waiting for an answer"
		}
	case "error":
		if status == "" && res.Err != nil {
			status = res.Err.Error()
		}
	}

	w.m.mu.Lock()
	if w.state != StateStopped {
		w.state, w.phase = state, phase
		w.parked = state == StateParked
		w.failure = ""
		if res.Err != nil {
			w.failure = res.Err.Error()
		}
		w.emitLocked(state, nil)
	}
	w.m.mu.Unlock()

	// The state outlives the worker: a restart must show the same suspended or
	// parked view, and the last report stays readable in `pai service show`.
	if sb, ok := w.m.backend.(ServiceStateBackend); ok {
		if err := sb.SetServiceState(w.name, state, status); err != nil {
			w.emit("error", "could not persist the service state: "+err.Error())
		}
	}

	select {
	case w.activation <- serviceResult{err: res.Err}:
	default:
	}
	return ackErr
}

func (w *worker) ackWake(wake core.Wake, cause error) error {
	eb, ok := w.m.backend.(ServiceEventBackend)
	if !ok || w.sessionID == "" {
		return nil
	}
	ids := make([]int64, 0, len(wake.Events))
	for _, ev := range wake.Events {
		if ev.ID != 0 {
			ids = append(ids, ev.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if cause == nil {
		return eb.AckEvents(w.sessionID, ids)
	}
	var first error
	for _, id := range ids {
		if err := eb.NackEvent(w.sessionID, id, cause); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// StopService stops scheduling a resident session and persists the stopped
// state, so a restart does not resume it. A live activation is canceled rather
// than finished: stopping is an operator action, not a request to complete the
// current task. Pending events stay in the store and are delivered after Start.
func (m *Manager) StopService(name string) error {
	if strings.TrimSpace(name) == "" {
		return ErrNotFound
	}
	m.mu.Lock()
	if m.closed || m.ctx.Err() != nil {
		m.mu.Unlock()
		return ErrClosed
	}
	if !m.serviceNames[name] {
		m.mu.Unlock()
		return ErrNotFound
	}
	if m.stoppedServices[name] {
		m.mu.Unlock()
		return nil
	}
	m.stoppedServices[name] = true
	m.mu.Unlock()

	// Unregister first so the scheduler cannot start another activation while the
	// current worker is being retired.
	m.scheduler.Unregister(name)
	for {
		m.mu.Lock()
		w := m.entries[name]
		m.mu.Unlock()
		if w == nil {
			break
		}
		w.cancel()
		<-w.done
		m.mu.Lock()
		if m.entries[name] == w {
			delete(m.entries, name)
		}
		m.mu.Unlock()
		break
	}

	if sb, ok := m.backend.(ServiceStateBackend); ok {
		if err := sb.SetServiceState(name, StateStopped, "stopped by user"); err != nil {
			return err
		}
	}
	if eb, ok := m.backend.(ServiceEventBackend); ok {
		if err := eb.SetNextWake(name, nil); err != nil {
			return err
		}
	}
	return nil
}

// StartService resumes a stopped service. Registration is due now, so the first
// activation probes immediately; the worker itself is prepared lazily by
// Activate, as it is after a restart.
func (m *Manager) StartService(name string) error {
	if strings.TrimSpace(name) == "" {
		return ErrNotFound
	}
	m.mu.Lock()
	if m.closed || m.ctx.Err() != nil {
		m.mu.Unlock()
		return ErrClosed
	}
	if !m.serviceNames[name] {
		m.mu.Unlock()
		return ErrNotFound
	}
	delete(m.stoppedServices, name)
	m.mu.Unlock()

	now := time.Now()
	if eb, ok := m.backend.(ServiceEventBackend); ok {
		if err := eb.SetNextWake(name, &now); err != nil {
			return err
		}
	}
	m.scheduler.Register(name, &now)
	if sb, ok := m.backend.(ServiceStateBackend); ok {
		if err := sb.SetServiceState(name, StateSuspended, ""); err != nil {
			return err
		}
	}
	return nil
}

// hasUserInput reports whether the wake carries an instruction from a person.
// Such a wake takes priority over due probes: the user is waiting for a reply.
func hasUserInput(events []core.Event) bool {
	for _, ev := range events {
		if ev.Class == core.EventUser && ev.Kind == "user_input" {
			return true
		}
	}
	return false
}

// nextWatcherRun reports when the scheduler should look at the probes again
// without running them now. An overdue probe returns now, so the activation that
// just answered the user is followed immediately by a normal probe pass.
func (m *Manager) nextWatcherRun(w *worker, now time.Time) *time.Time {
	m.mu.Lock()
	id, name := w.sessionID, w.name
	m.mu.Unlock()
	wb, ok := m.backend.(ServiceWatcherBackend)
	if !ok {
		return m.nextServiceWake(name, nil, now)
	}
	if id == "" {
		return nil
	}
	watchers, err := wb.Watchers(id)
	if err != nil {
		retry := now.Add(30 * time.Second)
		return &retry
	}
	// A user wake skips the probe pass, but a report interval still needs its
	// reserved watcher: materialize it here so the heartbeat is not lost.
	manifest := m.serviceManifest(name)
	if manifest != nil && manifest.ReportInterval > 0 && !hasWatcher(watchers, config.ReportWatcherName) {
		if added, ok := m.ensureReportWatcher(wb, id, manifest.ReportInterval, now); ok {
			watchers = append(watchers, added)
		}
	}
	var next *time.Time
	for _, ws := range watchers {
		if !ws.Enabled {
			continue
		}
		if ws.NextRun == nil || !ws.NextRun.After(now) {
			return &now
		}
		next = earlier(next, ws.NextRun)
	}
	return next
}

// renderSituation is the host's bounded projection of a wake. Telemetry never
// enters the model's context, the scheduler's own clock tick is dropped (the
// probe that fired explains the wake), and the rest is named so the model can
// tell a watcher apart from a user instruction. A probe's output is whatever it
// printed after its 1/0 decision, capped so one noisy script cannot dominate the
// prompt.
func renderSituation(wake core.Wake) string {
	var b strings.Builder
	b.WriteString("## Situation\n")
	if wake.Reason != "" {
		b.WriteString("Wake: " + wake.Reason + "\n")
	}
	for _, ev := range wake.Events {
		if ev.Class == core.EventTelemetry {
			continue
		}
		if ev.Class == core.EventTimer && ev.Source == "clock" {
			continue
		}
		switch {
		case ev.Class == core.EventWatcher:
			fmt.Fprintf(&b, "- watcher %q fired: %s\n",
				strings.TrimPrefix(ev.Source, "watch:"), boundedPayload(ev.Payload, "triggered"))
		case ev.Class == core.EventTimer && strings.HasPrefix(ev.Source, "watch:"):
			name := strings.TrimPrefix(ev.Source, "watch:")
			payload := boundedPayload(ev.Payload, "")
			if name == config.ReportWatcherName {
				fmt.Fprintf(&b, "- periodic report is due: %s\n", boundedPayload(ev.Payload, "report"))
			} else if payload != "" {
				fmt.Fprintf(&b, "- timer %q fired: %s\n", name, payload)
			} else {
				fmt.Fprintf(&b, "- timer %q fired\n", name)
			}
		case ev.Class == core.EventUser:
			if ev.Kind == "user_input" {
				// A user instruction is recorded as a turn, so the history already
				// carries it; repeating it here would send it twice.
				continue
			}
			fmt.Fprintf(&b, "- user: %s\n", boundedPayload(ev.Payload, "instruction"))
		default:
			fmt.Fprintf(&b, "- [%s] %s: %s\n", ev.Class, ev.Source, boundedPayload(ev.Payload, ""))
		}
	}
	const limit = 4 << 10
	if b.Len() > limit {
		return b.String()[:limit] + "\n[truncated]"
	}
	return b.String()
}

// boundedPayload caps one event's text and falls back to a label when a probe
// printed only its decision token.
func boundedPayload(payload, fallback string) string {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return fallback
	}
	if len(payload) > 512 {
		payload = strings.ToValidUTF8(payload[:512], "") + "…"
	}
	return payload
}

// Keep the compiler honest about the service-mode interface.
var _ ServiceHost = (*Manager)(nil)
