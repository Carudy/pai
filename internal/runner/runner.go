// Package runner hosts transport-independent, concurrent role runtimes.
package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
	"github.com/Carudy/pai/internal/role"
)

type Meta struct {
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Model     string    `json:"model"`
	Cwd       string    `json:"cwd"`
	UpdatedAt time.Time `json:"updated_at"`
}
type History struct {
	Meta
	Turns []core.Turn `json:"turns"`
	Total int         `json:"total"`
}

// Prepare transfers ownership of its resources to the worker. Cleanup is called
// exactly once, including when Prepare returns an error with a cleanup function.
// Backend methods must be concurrency-safe; Prepare must return promptly.
type Backend interface {
	List() ([]Meta, error)
	History(name string, offset, limit int) (History, error)
	Prepare(name string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), error)
}

// WorkspaceBackend resolves the immutable workspace when preparing a runtime.
// Resource ownership follows Backend.Prepare, including on failure.
type WorkspaceBackend interface {
	PrepareWorkspace(name, cwd string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), string, error)
}

type CreationBackend interface {
	Create(name, cwd string) (Meta, error)
}

// RoleBackend exposes role definitions without coupling transports to prompts.
type RoleBackend interface {
	Roles() ([]string, string, error)
	CreateWithRole(name, cwd, role string) (Meta, error)
}

func (m *Manager) Roles() ([]string, string, error) {
	if b, ok := m.backend.(RoleBackend); ok {
		return b.Roles()
	}
	return nil, "", errors.New("backend does not support roles")
}

// ResolveWorkingDir validates an existing directory without changing process cwd.
func ResolveWorkingDir(cwd string) (string, error) {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("stat working directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("working directory is not a directory")
	}
	return dir, nil
}

type Prompt struct {
	ID    string         `json:"id"`
	Kind  string         `json:"kind"`
	Title string         `json:"title"`
	Tool  *core.ToolCall `json:"tool,omitempty"`
}

func clonePrompt(p *Prompt) *Prompt {
	if p == nil {
		return nil
	}
	copy := *p
	if p.Tool != nil {
		tool := *p.Tool
		copy.Tool = &tool
	}
	return &copy
}

type Snapshot struct {
	Usage      core.Usage     `json:"usage"`
	TotalUsage core.Usage     `json:"total_usage"`
	UsageCalls int            `json:"usage_calls"`
	Name       string         `json:"name"`
	Model      string         `json:"model"`
	State      string         `json:"state"` // starting, busy, awaiting, stopped
	Phase      string         `json:"phase"`
	ActiveTool *core.ToolCall `json:"active_tool,omitempty"`
	Queued     int            `json:"queued"`
	Pending    *Prompt        `json:"pending,omitempty"`
	Error      string         `json:"error,omitempty"`
	Reasoning  string         `json:"reasoning,omitempty"`
}
type Event struct {
	Type     string   `json:"type"`
	Data     any      `json:"data,omitempty"`
	Snapshot Snapshot `json:"snapshot"`
}

// Activity identifies a live event independently of per-session subscribers.
type Activity struct {
	Name  string
	Event Event
}

var (
	ErrClosed    = errors.New("runner closed")
	ErrCapacity  = errors.New("runner capacity reached")
	ErrNotFound  = errors.New("runtime not found")
	ErrQueueFull = errors.New("runner queue full")
	ErrPrompt    = errors.New("prompt is no longer pending")
)

const queueLimit = 32
const subscriberLimit = 32
const eventLimit = 64
const reasoningLimit = 64 << 10

// ModelBackend is optional; model discovery must not require remote API calls.
type ModelBackend interface {
	Models() ([]string, string, error)
	SetModel(name, model string) error
}

var ErrBusy = errors.New("session is busy; wait for pending prompts and queued instructions before changing model")

func (m *Manager) Models() ([]string, string, error) {
	if b, ok := m.backend.(ModelBackend); ok {
		return b.Models()
	}
	return nil, "", errors.New("backend does not support models")
}

// SetModel retires the idle runtime before persisting settings. The reservation
// survives releasing mu for the join, so no new runtime can load stale metadata.
func (m *Manager) SetModel(name, model string) error {
	m.mu.Lock()
	if m.closed || m.ctx.Err() != nil {
		m.mu.Unlock()
		return ErrClosed
	}
	b, ok := m.backend.(ModelBackend)
	if !ok {
		m.mu.Unlock()
		return errors.New("backend does not support models")
	}
	if m.changing[name] {
		m.mu.Unlock()
		return ErrBusy
	}
	w := m.entries[name]
	if w != nil && (w.state != "awaiting" || w.pending != nil || len(w.tasks) != 0 || len(w.steering) != 0) {
		m.mu.Unlock()
		return ErrBusy
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
	defer m.mu.Unlock()
	defer delete(m.changing, name)
	if m.closed || m.ctx.Err() != nil {
		return ErrClosed
	}
	return b.SetModel(name, model)
}

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
}
type answer struct {
	text    string
	approve bool
	aborted bool
}
type worker struct {
	m            *Manager
	name         string
	workingDir   string
	model        string
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	tasks        chan string
	steering     chan string
	rt           *role.Runtime
	state        string
	phase        string
	reasoning    string
	usage        core.Usage
	totalUsage   core.Usage
	usageCalls   int
	activeTool   *core.ToolCall
	failure      string
	instruction  bool
	abortPrompts bool
	latestTool   *core.ToolCall
	pending      *Prompt
	reply        chan answer
	subs         map[chan Event]struct{}
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
		return b.CreateWithRole(name, cwd, role)
	}
	if role != "" {
		return Meta{}, errors.New("backend does not support roles")
	}
	b, ok := m.backend.(CreationBackend)
	if !ok {
		return Meta{}, errors.New("backend does not support session creation")
	}
	return b.Create(name, cwd)
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
func (m *Manager) Reply(name, id, text string, approve bool) error {
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
	w.reply <- answer{text: text, approve: approve}
	if w.pending.Kind == "confirm" && w.activeTool != nil {
		w.phase = "tool"
	} else {
		w.phase = "waiting_model"
	}
	w.pending = nil
	w.emitLocked("prompt_replied", nil)
	return nil
}
func (w *worker) snapshotLocked() Snapshot {
	s := Snapshot{Name: w.name, Model: w.model, State: w.state, Queued: len(w.tasks) + len(w.steering), Error: w.failure}
	s.Phase = w.phase
	s.Reasoning = w.reasoning
	s.Usage, s.TotalUsage, s.UsageCalls = w.usage, w.totalUsage, w.usageCalls
	if w.activeTool != nil {
		tool := *w.activeTool
		s.ActiveTool = &tool
	}
	s.Pending = clonePrompt(w.pending)
	if w.pending != nil {
		if w.pending.Kind == "confirm" {
			s.Phase = "waiting_approval"
		} else {
			s.Phase = "waiting_question"
		}
	}
	return s
}
func (m *Manager) Snapshot(name string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.entries[name]
	if w == nil {
		return Snapshot{}, ErrNotFound
	}
	return w.snapshotLocked(), nil
}

// Subscribe atomically installs a subscriber and delivers a snapshot. Slow
// subscribers are disconnected rather than blocking the runtime; reconnect to
// recover current state and use History to recover durable turns.
func (m *Manager) Subscribe(name string) (<-chan Event, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, ErrClosed
	}
	w := m.entries[name]
	if w == nil {
		return nil, nil, ErrNotFound
	}
	if len(w.subs) >= subscriberLimit {
		return nil, nil, ErrCapacity
	}
	ch := make(chan Event, eventLimit)
	w.subs[ch] = struct{}{}
	ch <- Event{Type: "snapshot", Snapshot: w.snapshotLocked()}
	return ch, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := w.subs[ch]; ok {
			delete(w.subs, ch)
			close(ch)
		}
	}, nil
}

// SubscribeActivities observes all future worker events without replay. Its
// bounded buffer drops new events when full, never delaying agents. Unsubscribe
// is idempotent; Close closes the channel after workers finish emitting.
func (m *Manager) SubscribeActivities() (<-chan Activity, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, ErrClosed
	}
	if len(m.activities) >= subscriberLimit {
		return nil, nil, ErrCapacity
	}
	if m.activities == nil {
		m.activities = make(map[chan Activity]struct{})
	}
	ch := make(chan Activity, 128)
	m.activities[ch] = struct{}{}
	return ch, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.activities[ch]; ok {
			delete(m.activities, ch)
			close(ch)
		}
	}, nil
}

func (w *worker) eventLocked(kind string, data any) Event {
	if p, ok := data.(Prompt); ok {
		data = *clonePrompt(&p)
	}
	snapshot := w.snapshotLocked()
	// Deltas already carry reasoning; only recovery snapshots need the full tail.
	snapshot.Reasoning = ""
	return Event{Type: kind, Data: data, Snapshot: snapshot}
}

func (w *worker) emitLocked(kind string, data any) {
	if kind != "reasoning" && kind != "notice" {
		w.reasoning = ""
	}
	for ch := range w.m.activities {
		select {
		case ch <- Activity{Name: w.name, Event: w.eventLocked(kind, data)}:
		default:
		}
	}
	for ch := range w.subs {
		// Each recipient owns its payload; mutating one event cannot change the
		// pending approval or another subscriber's reconnect snapshot.
		e := w.eventLocked(kind, data)
		select {
		case ch <- e:
		default:
			delete(w.subs, ch)
			close(ch)
		}
	}
}
func (w *worker) emit(kind string, data any) {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	w.emitLocked(kind, data)
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
func (w *worker) run() {
	defer w.m.wg.Done()
	defer func() {
		w.cancel()
		w.m.mu.Lock()
		w.state = "stopped"
		w.phase = "stopped"
		w.activeTool = nil
		w.latestTool = nil
		w.pending = nil
		w.emitLocked("stopped", nil)
		for ch := range w.subs {
			delete(w.subs, ch)
			close(ch)
		}
		delete(w.m.entries, w.name)
		close(w.done)
		w.m.mu.Unlock()
	}()
	var cfg *config.UserConfig
	var p provider.Provider
	var rec core.Recorder
	var history []provider.Message
	var cleanup func()
	var cwd string
	var err error
	if b, ok := w.m.backend.(WorkspaceBackend); ok {
		cfg, p, rec, history, cleanup, cwd, err = b.PrepareWorkspace(w.name, w.workingDir)
	} else {
		cfg, p, rec, history, cleanup, err = w.m.backend.Prepare(w.name)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil && (cfg == nil || p == nil) {
		err = errors.New("backend returned incomplete runtime")
	}
	if err == nil && w.ctx.Err() == nil {
		copyCfg := *cfg
		rt := &role.Runtime{WorkingDir: cwd, Provider: p, Recorder: rec, Observer: observer{w}, Prompter: w, Logger: w, Interactive: true, SessionName: w.name}
		w.m.mu.Lock()
		w.rt = rt
		w.model = copyCfg.DefaultModel
		w.workingDir = cwd
		if w.state != "stopped" {
			w.state = "busy"
			w.phase = "waiting_model"
		}
		w.m.mu.Unlock()
		select {
		case text := <-w.tasks:
			err = role.Run(w.ctx, &copyCfg, rt, history, text)
		case <-w.ctx.Done():
			err = w.ctx.Err()
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		w.m.mu.Lock()
		w.failure = err.Error()
		w.emitLocked("error", err.Error())
		w.m.mu.Unlock()
	}
}
func (w *worker) Awaiting() {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	w.instruction = true
	w.latestTool = nil
	w.activeTool = nil
	w.phase = "awaiting"
	w.abortPrompts = false
	if w.state != "stopped" {
		w.state = "awaiting"
	}
	w.emitLocked("awaiting", nil)
}
func (w *worker) Ask(title string) (string, error) {
	w.m.mu.Lock()
	instruction := w.instruction
	w.instruction = false
	w.m.mu.Unlock()
	if !instruction {
		a, err := w.prompt("ask", title)
		return a.text, err
	}
	var text string
	select {
	case text = <-w.tasks:
	case text = <-w.steering:
	case <-w.ctx.Done():
		return "", core.ErrAborted
	}
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	if w.ctx.Err() != nil {
		return "", core.ErrAborted
	}
	w.state = "busy"
	w.phase = "waiting_model"
	w.activeTool = nil
	w.emitLocked("busy", nil)
	return text, nil
}
func (w *worker) prompt(kind, title string) (answer, error) {
	w.m.mu.Lock()
	tool := w.latestTool
	w.latestTool = nil // A tool can belong to only its immediately following prompt.
	if w.ctx.Err() != nil || w.abortPrompts {
		w.m.mu.Unlock()
		return answer{}, core.ErrAborted
	}
	if kind == "ask" {
		w.activeTool = nil
		w.phase = "waiting_model"
	}
	w.m.sequence++
	w.pending = &Prompt{ID: fmt.Sprint(w.m.sequence), Kind: kind, Title: title}
	if kind == "confirm" && tool != nil {
		copy := *tool
		w.pending.Tool = &copy
	}
	ch := make(chan answer, 1)
	w.reply = ch
	w.emitLocked("prompt", *w.pending)
	w.m.mu.Unlock()
	var a answer
	select {
	case a = <-ch:
	case <-w.ctx.Done():
		a.aborted = true
	}
	w.m.mu.Lock()
	if w.reply == ch {
		w.pending = nil
		w.reply = nil
	}
	w.m.mu.Unlock()
	if a.aborted {
		return a, core.ErrAborted
	}
	return a, nil
}
func (w *worker) Confirm(title string) (bool, error) {
	a, err := w.prompt("confirm", title)
	return a.approve, err
}
func (w *worker) Steer() (string, bool) {
	select {
	case text := <-w.steering:
		return text, true
	default:
		return "", false
	}
}
func (w *worker) Reason(s string)    { w.modelPhase("reason", s) }
func (w *worker) Done(s string)      { w.finish("done", s) }
func (w *worker) Terminate(s string) { w.finish("terminate", s) }

func (w *worker) finish(kind, s string) {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	w.phase = "awaiting"
	w.activeTool = nil
	w.latestTool = nil
	w.emitLocked(kind, s)
}

func (w *worker) modelPhase(kind string, data any) {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	// Usage ends provider streaming, not the action or the whole run.
	if w.activeTool == nil {
		w.phase = "waiting_model"
		if kind == "reasoning" {
			w.phase = "reasoning"
		}
	}
	w.emitLocked(kind, data)
}
func (w *worker) AskEvent(s string) { w.emit("ask", s) }
func (w *worker) User(s string)     { w.emit("user", s) }
func (w *worker) Session(s string)  { w.emit("session", s) }
func (w *worker) Reasoning(s string) {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	w.reasoning += s
	if len(w.reasoning) > reasoningLimit {
		start := len(w.reasoning) - reasoningLimit
		// Don't split a UTF-8 token when retaining a bounded tail.
		for start < len(w.reasoning) && w.reasoning[start]&0xc0 == 0x80 {
			start++
		}
		w.reasoning = w.reasoning[start:]
	}
	if w.activeTool == nil {
		w.phase = "reasoning"
	}
	w.emitLocked("reasoning", s)
}
func (w *worker) Usage(u core.Usage) {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	// Count observer reports once at the source, never during snapshot replay.
	w.usage = u
	w.totalUsage.Prompt += u.Prompt
	w.totalUsage.Completion += u.Completion
	w.totalUsage.Total += u.Total
	w.usageCalls++
	if w.activeTool == nil {
		w.phase = "waiting_model"
	}
	w.emitLocked("usage", u)
}
func (w *worker) ToolCall(c core.ToolCall) {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	w.latestTool = &c
	active := c
	w.activeTool = &active
	w.phase = "tool"
	w.emitLocked("tool_call", c)
}
func (w *worker) ToolOutput(s string) { w.emit("tool_output", s) }
func (w *worker) ToolResult(r core.ToolResult) {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	w.latestTool = nil
	w.activeTool = nil
	w.phase = "waiting_model"
	w.emitLocked("tool_result", r)
}
func (w *worker) Notice(s string)           { w.emit("notice", s) }
func (w *worker) Output(s string)           { w.emit("output", s) }
func (w *worker) Separator()                { w.emit("separator", nil) }
func (w *worker) Debugf(string, ...any)     {}
func (w *worker) Errorf(f string, a ...any) { w.emit("notice", fmt.Sprintf(f, a...)) }

// Observer and Prompter have incompatible Ask methods, so the observer delegates
// every other method to the worker and overrides only the semantic Ask event.
type observer struct{ *worker }

func (o observer) Ask(s string) { o.AskEvent(s) }

var _ core.Observer = observer{}
var _ core.Prompter = (*worker)(nil)
var _ core.Steerer = (*worker)(nil)
