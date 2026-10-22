package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

var (
	ErrClosed    = errors.New("runner closed")
	ErrCapacity  = errors.New("runner capacity reached")
	ErrNotFound  = errors.New("runtime not found")
	ErrQueueFull = errors.New("runner queue full")
	ErrPrompt    = errors.New("prompt is no longer pending")
)

var ErrBusy = errors.New("session is busy; wait for pending prompts and queued instructions before changing model")

const queueLimit = 32
const subscriberLimit = 32
const eventLimit = 64
const reasoningLimit = 64 << 10
const failureSnapshotLimit = 32

type Manager struct {
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	backend    Backend
	max        int
	closed     bool
	entries    map[string]*worker
	changing   map[string]bool
	wg         sync.WaitGroup
	sequence   uint64
	activities map[chan Activity]struct{}
	// Failures survive worker removal without occupying runtime slots.
	failures []Snapshot
	// trustAll is a per-session auto-approval toggle. It lives here, not on the
	// worker, because the worker is recreated for each instruction while the
	// setting must last for the session.
	trustAll map[string]bool
	// commandTrust is the per-session grant of command names the user allowed for
	// this conversation: a "trust session" answer, or the composer's sudo switch. It
	// lives here rather than on the runtime for the same reason as trustAll — the
	// runtime is retired by any settings change, while the grant is about the
	// conversation and should outlast that.
	commandTrust map[string]map[string]bool
	// reasoning is a per-session reasoning-effort override, in memory only. It
	// lives here for the same reason as trustAll: it must outlast the worker
	// recreated for each instruction.
	reasoning map[string]string
}

func New(ctx context.Context, backend Backend, max int) *Manager {
	if max < 1 {
		max = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	m := &Manager{ctx: ctx, cancel: cancel, backend: backend, max: max, entries: make(map[string]*worker)}
	go func() { <-ctx.Done(); m.Close() }()
	return m
}
func (m *Manager) List() ([]Meta, error) { return m.backend.List() }
func (m *Manager) History(name string, offset, limit int) (History, error) {
	return m.backend.History(name, offset, limit)
}

// Context reports a window of the context a session would send on its next
// request. It reads persisted state only, so it works whether or not a runtime
// is live.
func (m *Manager) Context(name string, offset, limit int) (ContextView, error) {
	b, ok := m.backend.(ContextBackend)
	if !ok {
		return ContextView{}, errors.New("backend does not expose context")
	}
	return b.Context(name, offset, limit)
}

// SetOverride records the user's surgery on one stored turn and retires the idle
// runtime, so the next instruction re-prepares its history with the change.
func (m *Manager) SetOverride(name string, seq int, action, content string) error {
	b, ok := m.backend.(ContextSetter)
	if !ok {
		return errors.New("backend does not support context overrides")
	}
	finish, err := m.reserveIdle(name)
	if err != nil {
		return err
	}
	defer finish()
	if err := b.SetOverride(name, seq, action, content); err != nil {
		return err
	}
	m.clearFailureLocked(name)
	return nil
}

// DeleteOverride clears the user's surgery on one stored turn, retiring the idle
// runtime so the next instruction re-prepares with the stored text.
func (m *Manager) DeleteOverride(name string, seq int) error {
	b, ok := m.backend.(ContextSetter)
	if !ok {
		return errors.New("backend does not support context overrides")
	}
	finish, err := m.reserveIdle(name)
	if err != nil {
		return err
	}
	defer finish()
	if err := b.DeleteOverride(name, seq); err != nil {
		return err
	}
	m.clearFailureLocked(name)
	return nil
}

// SetEssential marks or unmarks one stored turn as essential, retiring the idle
// runtime so the next instruction re-prepares without ever eliding it.
func (m *Manager) SetEssential(name string, seq int, on bool) error {
	b, ok := m.backend.(ContextSetter)
	if !ok {
		return errors.New("backend does not support context overrides")
	}
	finish, err := m.reserveIdle(name)
	if err != nil {
		return err
	}
	defer finish()
	if err := b.SetEssential(name, seq, on); err != nil {
		return err
	}
	m.clearFailureLocked(name)
	return nil
}

// Send queues an instruction, never an answer to a model question. At capacity
// an idle worker is evicted and joined before its slot is reused.
func (m *Manager) Send(name, text string) error {
	return m.SendWithWorkspace(name, text, "")
}

func (m *Manager) Create(name, cwd string) (Meta, error) {
	return m.CreateWithRole(name, cwd, "")
}

func (m *Manager) CreateWithRole(name, cwd, role string) (Meta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return Meta{}, ErrClosed
	}
	if strings.TrimSpace(name) == "" {
		return Meta{}, errors.New("session name is empty")
	}
	if b, ok := m.backend.(RoleBackend); ok {
		meta, err := b.CreateWithRole(name, cwd, role)
		if err == nil {
			m.clearFailureLocked(name)
		}
		return meta, err
	}
	if role != "" {
		return Meta{}, errors.New("backend does not support roles")
	}
	b, ok := m.backend.(CreationBackend)
	if !ok {
		return Meta{}, errors.New("backend does not support session creation")
	}
	meta, err := b.Create(name, cwd)
	if err == nil {
		m.clearFailureLocked(name)
	}
	return meta, err
}

// SetTrustAll turns a session's trust-all mode on or off. While it is on, every
// command and file-change confirmation is approved without a prompt. The flag is
// in-memory only and never persisted, so a restart re-asks. Turning it on also
// answers a confirmation that is already waiting, so the switch takes effect at
// once rather than after the user also clicks the pending card.
// SetCommandTrust grants or revokes one command name for a session. Trust is
// name-based, so the name must be a bare command name: a path or a whole command
// line would not mean what it looks like.
func (m *Manager) SetCommandTrust(name, cmd string, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return ErrClosed
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("session name is empty")
	}
	if !m.knownSessionLocked(name) {
		return ErrNotFound
	}
	command, err := commandName(cmd)
	if err != nil {
		return err
	}
	if m.commandTrust == nil {
		m.commandTrust = make(map[string]map[string]bool)
	}
	if on {
		if m.commandTrust[name] == nil {
			m.commandTrust[name] = make(map[string]bool)
		}
		m.commandTrust[name][command] = true
	} else {
		delete(m.commandTrust[name], command)
	}
	return nil
}

// CommandTrust returns the names granted for a session, sorted.
func (m *Manager) CommandTrust(name string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return commandTrustNames(m.commandTrust[name])
}

// grantCommandTrust records what a confirmation answered, so that a grant the user
// just made is not forgotten when the runtime that asked is retired.
func (m *Manager) grantCommandTrust(name string, names []string) {
	if len(names) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	if m.commandTrust == nil {
		m.commandTrust = make(map[string]map[string]bool)
	}
	if m.commandTrust[name] == nil {
		m.commandTrust[name] = make(map[string]bool)
	}
	for _, n := range names {
		if command, err := commandName(n); err == nil {
			m.commandTrust[name][command] = true
		}
	}
}

// commandTrustLocked is commandTrust for a caller already holding the lock.
func (m *Manager) commandTrustLocked(name string) []string {
	return commandTrustNames(m.commandTrust[name])
}
func commandTrustNames(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// commandName validates a name for a session-scoped trust grant.
func commandName(cmd string) (string, error) {
	name := strings.TrimSpace(cmd)
	if name == "" {
		return "", errors.New("command name is empty")
	}
	if len(name) > 64 {
		return "", errors.New("command name is too long")
	}
	if strings.ContainsAny(name, " \t\n/") {
		return "", errors.New("command name must be a bare name, without spaces or a path")
	}
	return name, nil
}

func (m *Manager) SetTrustAll(name string, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return ErrClosed
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("session name is empty")
	}
	if !m.knownSessionLocked(name) {
		return ErrNotFound
	}
	if m.trustAll == nil {
		m.trustAll = make(map[string]bool)
	}
	if on {
		m.trustAll[name] = true
	} else {
		delete(m.trustAll, name)
	}
	w := m.entries[name]
	if w == nil {
		return nil
	}
	if on && w.pending != nil && w.pending.Kind == "confirm" && w.reply != nil {
		w.reply <- answer{choice: core.TrustOnce}
		if w.activeTool != nil {
			w.phase = "tool"
		} else {
			w.phase = "waiting_model"
		}
		w.pending = nil
		w.emitLocked("prompt_replied", nil)
	}
	return nil
}

// TrustAll reports whether a session is in trust-all mode. An unknown session is
// simply off.
func (m *Manager) TrustAll(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.trustAll[name]
}

// knownSessionLocked reports whether name is a live worker, a retained failure,
// or a stored session, so a mutation never targets a name that cannot exist.
func (m *Manager) knownSessionLocked(name string) bool {
	if m.entries[name] != nil {
		return true
	}
	for _, s := range m.failures {
		if s.Name == name {
			return true
		}
	}
	if m.backend == nil {
		return false
	}
	list, err := m.backend.List()
	if err != nil {
		return false
	}
	for _, meta := range list {
		if meta.Name == name {
			return true
		}
	}
	return false
}

func (m *Manager) SendWithWorkspace(name, text, cwd string) error {
	if cwd != "" {
		if _, ok := m.backend.(WorkspaceBackend); !ok {
			return errors.New("backend does not support working directories")
		}
		var err error
		cwd, err = ResolveWorkingDir(cwd)
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("session name is empty")
	}
	if err := validate(text); err != nil {
		return err
	}
	for {
		m.mu.Lock()
		if m.closed || m.ctx.Err() != nil {
			m.mu.Unlock()
			return ErrClosed
		}
		if m.changing[name] {
			m.mu.Unlock()
			return ErrBusy
		}
		if w := m.entries[name]; w != nil {
			if w.state == "stopped" {
				done := w.done
				m.mu.Unlock()
				<-done
				continue
			}
			if cwd != "" && cwd != w.workingDir {
				m.mu.Unlock()
				return errors.New("requested working directory differs from session workspace")
			}
			select {
			case w.tasks <- text:
				m.clearFailureLocked(name)
				// The worker may dequeue before reacquiring mu; reserve busy now.
				if w.state == "awaiting" {
					w.state = "busy"
				}
				w.emitLocked("queued", nil)
				m.mu.Unlock()
				return nil
			default:
				m.mu.Unlock()
				return ErrQueueFull
			}
		}
		if len(m.entries) >= m.max {
			var idle *worker
			for _, w := range m.entries {
				if w.state == "awaiting" && len(w.tasks) == 0 && len(w.steering) == 0 {
					idle = w
					break
				}
			}
			if idle == nil {
				m.mu.Unlock()
				return ErrCapacity
			}
			idle.state = "stopped"
			idle.cancel()
			done := idle.done
			m.mu.Unlock()
			<-done
			continue
		}
		ctx, cancel := context.WithCancel(m.ctx)
		w := &worker{m: m, name: name, workingDir: cwd, ctx: ctx, cancel: cancel, done: make(chan struct{}), tasks: make(chan string, queueLimit), steering: make(chan string, queueLimit), state: "starting", phase: "starting", subs: make(map[chan Event]struct{})}
		w.tasks <- text
		m.clearFailureLocked(name)
		m.entries[name] = w
		m.wg.Add(1)
		m.mu.Unlock()
		go w.run()
		return nil
	}
}
func (m *Manager) Steer(name, text string) error {
	if err := validate(text); err != nil {
		return err
	}
	// Slash commands have no command-dispatch semantics at steering safe points.
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		return errors.New("slash commands cannot steer")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	w := m.entries[name]
	if w == nil {
		return ErrNotFound
	}
	if w.state == "stopped" {
		return ErrNotFound
	}
	ch := w.steering
	if w.state == "awaiting" {
		ch = w.tasks
	}
	select {
	case ch <- text:
		if w.state == "awaiting" {
			w.state = "busy"
		}
		w.emitLocked("queued", nil)
		return nil
	default:
		return ErrQueueFull
	}
}
func (m *Manager) Cancel(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	w := m.entries[name]
	if w == nil {
		return ErrNotFound
	}
	w.latestTool = nil
	if w.state != "awaiting" {
		w.abortPrompts = true
		if w.rt != nil {
			w.rt.Interrupt()
		} else {
			w.cancel()
		}
	}
	if w.pending != nil {
		w.reply <- answer{aborted: true}
		w.pending = nil
		w.emitLocked("prompt_cancelled", nil)
	}
	return nil
}
func (m *Manager) Reply(name, id, text string, choice core.TrustChoice) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	w := m.entries[name]
	if w == nil {
		return ErrNotFound
	}
	if w.pending == nil || w.pending.ID != id {
		return ErrPrompt
	}
	w.reply <- answer{text: text, choice: choice}
	if w.pending.Kind == "confirm" && w.activeTool != nil {
		w.phase = "tool"
	} else {
		w.phase = "waiting_model"
	}
	w.pending = nil
	w.emitLocked("prompt_replied", nil)
	return nil
}

// SetModel retires the idle runtime before persisting settings, so the next
// instruction uses the new model.
func (m *Manager) SetModel(name, model string) error {
	b, ok := m.backend.(ModelBackend)
	if !ok {
		return errors.New("backend does not support models")
	}
	finish, err := m.reserveIdle(name)
	if err != nil {
		return err
	}
	defer finish()
	if err := b.SetModel(name, model); err != nil {
		return err
	}
	m.clearFailureLocked(name)
	return nil
}

// SetRole retires the idle runtime before persisting the new role, so the next
// instruction rebuilds its prompt and tool set with the new role.
func (m *Manager) SetRole(name, role string) error {
	b, ok := m.backend.(RoleSetter)
	if !ok {
		return errors.New("backend does not support changing roles")
	}
	finish, err := m.reserveIdle(name)
	if err != nil {
		return err
	}
	defer finish()
	if err := b.SetRole(name, role); err != nil {
		return err
	}
	m.clearFailureLocked(name)
	return nil
}

// SetWorkingDir retires the idle runtime before persisting its workspace, so the
// next instruction rebuilds the runtime without changing process cwd.
func (m *Manager) SetWorkingDir(name, cwd string) error {
	if strings.TrimSpace(cwd) == "" {
		return errors.New("working directory is required")
	}
	dir, err := ResolveWorkingDir(cwd)
	if err != nil {
		return err
	}
	b, ok := m.backend.(WorkingDirSetter)
	if !ok {
		return errors.New("backend does not support changing working directories")
	}
	finish, err := m.reserveIdle(name)
	if err != nil {
		return err
	}
	defer finish()
	if err := b.SetWorkingDir(name, dir); err != nil {
		return err
	}
	m.clearFailureLocked(name)
	return nil
}

// Rename retires the idle runtime before renaming the session, so no live worker
// keeps serving under the old name, and drops the now-orphaned entry.
func (m *Manager) Rename(name, newName string) error {
	b, ok := m.backend.(RenameBackend)
	if !ok {
		return errors.New("backend does not support renaming")
	}
	finish, err := m.reserveIdle(name)
	if err != nil {
		return err
	}
	defer finish()
	if err := b.Rename(name, newName); err != nil {
		return err
	}
	m.clearFailureLocked(name)
	m.clearFailureLocked(newName)
	delete(m.entries, name)
	delete(m.trustAll, name)
	return nil
}

// Delete retires the idle runtime before removing the session from storage, so no
// live worker keeps serving a deleted session, and drops the orphaned entry.
func (m *Manager) Delete(name string) error {
	b, ok := m.backend.(DeleteBackend)
	if !ok {
		return errors.New("backend does not support deleting")
	}
	finish, err := m.reserveIdle(name)
	if err != nil {
		return err
	}
	defer finish()
	if err := b.Delete(name); err != nil {
		return err
	}
	m.clearFailureLocked(name)
	delete(m.entries, name)
	delete(m.trustAll, name)
	return nil
}

// Rewind drops every persisted turn from index keep onward, retiring the idle
// runtime so the next instruction re-prepares from the truncated history.
func (m *Manager) Rewind(name string, keep int, backup *bool) error {
	if keep < 0 {
		return errors.New("keep must be non-negative")
	}
	b, ok := m.backend.(RewindBackend)
	if !ok {
		return errors.New("backend does not support rewinding")
	}
	finish, err := m.reserveIdle(name)
	if err != nil {
		return err
	}
	defer finish()
	if err := b.Rewind(name, keep, backup); err != nil {
		return err
	}
	m.clearFailureLocked(name)
	return nil
}

func (m *Manager) Models() ([]string, string, error) {
	if b, ok := m.backend.(ModelBackend); ok {
		return b.Models()
	}
	return nil, "", errors.New("backend does not support models")
}

func (m *Manager) Roles() ([]string, string, error) {
	if b, ok := m.backend.(RoleBackend); ok {
		return b.Roles()
	}
	return nil, "", errors.New("backend does not support roles")
}

// Reasoning returns a session's effective reasoning effort: its in-memory
// override if set, otherwise the backend's configured default.
func (m *Manager) Reasoning(name string) (string, error) {
	m.mu.Lock()
	v := m.reasoning[name]
	m.mu.Unlock()
	if v != "" {
		return v, nil
	}
	if b, ok := m.backend.(ReasoningBackend); ok {
		return b.ReasoningDefault()
	}
	return string(provider.ReasoningEffortNone), nil
}

// SetReasoning stores a per-session reasoning override and retires the idle
// worker so the next run picks it up. The override is in-memory only: it is not
// persisted and is lost when the process restarts.
func (m *Manager) SetReasoning(name, effort string) error {
	switch provider.ReasoningEffort(effort) {
	case provider.ReasoningEffortNone, provider.ReasoningEffortLow, provider.ReasoningEffortMedium, provider.ReasoningEffortHigh:
	default:
		return fmt.Errorf("invalid reasoning effort %q", effort)
	}
	finish, err := m.reserveIdle(name)
	if err != nil {
		return err
	}
	defer finish()
	if m.reasoning == nil {
		m.reasoning = make(map[string]string)
	}
	m.reasoning[name] = effort
	return nil
}

func (m *Manager) Snapshot(name string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.entries[name]; w != nil {
		return w.snapshotLocked(), nil
	}
	for _, s := range m.failures {
		if s.Name == name {
			return cloneSnapshot(s), nil
		}
	}
	return Snapshot{}, ErrNotFound
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	for _, w := range m.entries {
		w.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.activities {
		delete(m.activities, ch)
		close(ch)
	}
}

// reserveIdle marks name as changing and retires its idle runtime, so the next
// instruction re-prepares with fresh metadata. On success m.mu is held and the
// caller must call the returned finish (typically deferred) after the backend
// change. The reservation survives releasing mu for the join, so no send can
// start a runtime that would load stale metadata (Send returns ErrBusy).
func (m *Manager) reserveIdle(name string) (func(), error) {
	m.mu.Lock()
	if m.closed || m.ctx.Err() != nil {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if m.changing[name] {
		m.mu.Unlock()
		return nil, ErrBusy
	}
	w := m.entries[name]
	if w != nil && (w.state != "awaiting" || w.pending != nil || len(w.tasks) != 0 || len(w.steering) != 0) {
		m.mu.Unlock()
		return nil, ErrBusy
	}
	if m.changing == nil {
		m.changing = make(map[string]bool)
	}
	m.changing[name] = true
	if w != nil {
		w.state = "stopped"
		w.cancel()
		m.mu.Unlock()
		<-w.done
		m.mu.Lock()
	}
	if m.closed || m.ctx.Err() != nil {
		delete(m.changing, name)
		m.mu.Unlock()
		return nil, ErrClosed
	}
	return func() {
		delete(m.changing, name)
		m.mu.Unlock()
	}, nil
}

func validate(text string) error {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return errors.New("instruction is empty")
	}
	line := strings.TrimSpace(text)
	if strings.HasPrefix(line, "/") && !strings.HasPrefix(line, "//") {
		command := strings.Fields(strings.TrimPrefix(line, "/"))
		if len(command) > 0 {
			switch strings.ToLower(command[0]) {
			case "new", "rename":
				return errors.New("session binding commands are unavailable in runner")
			}
		}
	}
	return nil
}

func (m *Manager) clearFailureLocked(name string) {
	for i, s := range m.failures {
		if s.Name == name {
			m.failures = slices.Delete(m.failures, i, i+1)
			return
		}
	}
}

func (m *Manager) rememberFailureLocked(s Snapshot) {
	m.clearFailureLocked(s.Name)
	if len(m.failures) == failureSnapshotLimit {
		m.failures = slices.Delete(m.failures, 0, 1)
	}
	// The queued instructions died with the worker and cannot be delivered.
	s.Queued = 0
	m.failures = append(m.failures, cloneSnapshot(s))
}
