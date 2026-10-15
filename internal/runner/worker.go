package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
	"github.com/Carudy/pai/internal/role"
)

type answer struct {
	text    string
	choice  core.TrustChoice
	aborted bool
}
type worker struct {
	m             *Manager
	name          string
	workingDir    string
	model         string
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	tasks         chan string
	steering      chan string
	rt            *role.Runtime
	state         string
	phase         string
	reasoning     string
	usage         core.Usage
	totalUsage    core.Usage
	usageCalls    int
	contextTokens int // compaction trigger from config; 0 = disabled
	activeTool    *core.ToolCall
	failure       string
	instruction   bool
	abortPrompts  bool
	latestTool    *core.ToolCall
	pending       *Prompt
	reply         chan answer
	subs          map[chan Event]struct{}
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
		if w.failure != "" {
			w.m.rememberFailureLocked(w.snapshotLocked())
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
		if v := w.reasoningOverride(); v != "" {
			copyCfg.ReasoningEffort = provider.ReasoningEffort(v)
		}
		rt := &role.Runtime{WorkingDir: cwd, Provider: p, Recorder: rec, Observer: observer{w}, Prompter: w, Logger: w, Interactive: true, SessionName: w.name}
		w.m.mu.Lock()
		w.rt = rt
		w.model = copyCfg.DefaultModel
		w.workingDir = cwd
		w.contextTokens = copyCfg.Context.SummarizeAfterTokens
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

func (w *worker) snapshotLocked() Snapshot {
	s := Snapshot{Name: w.name, Model: w.model, State: w.state, Queued: len(w.tasks) + len(w.steering), Error: w.failure}
	s.Phase = w.phase
	s.Reasoning = w.reasoning
	s.TrustAll = w.m.trustAll[w.name]
	s.Usage, s.TotalUsage, s.UsageCalls = w.usage, w.totalUsage, w.usageCalls
	s.ContextTokens = w.contextTokens
	if w.activeTool != nil {
		s.ActiveTool = cloneToolCall(w.activeTool)
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

// reasoningOverride returns the session's in-memory reasoning effort, or "" when
// it has none (the run then keeps the configured default).
func (w *worker) reasoningOverride() string {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	return w.m.reasoning[w.name]
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
		a, err := w.prompt("ask", title, nil, "")
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
func (w *worker) prompt(kind, title string, untrusted []string, target string) (answer, error) {
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
	w.pending = &Prompt{ID: fmt.Sprint(w.m.sequence), Kind: kind, Title: title, TrustTarget: target}
	if kind == "confirm" {
		w.pending.Tool = cloneToolCall(tool)
	}
	if len(untrusted) > 0 {
		w.pending.Untrusted = append([]string(nil), untrusted...)
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
	if w.approved() {
		return true, nil
	}
	a, err := w.prompt("confirm", title, nil, "")
	return a.choice != core.TrustDeny, err
}

// approved reports whether the session is in trust-all mode, in which every
// confirmation is granted without asking. The flag lives on the manager keyed by
// session name, so it outlives the worker that is recreated between instructions.
func (w *worker) approved() bool {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	return w.m.trustAll[w.name]
}

// ConfirmCommand implements core.CommandConfirmer: a command confirmation that
// also offers to trust the flagged command names for this run or future runs.
func (w *worker) ConfirmCommand(title string, untrusted []string) (core.TrustChoice, error) {
	if w.approved() {
		return core.TrustOnce, nil
	}
	a, err := w.prompt("confirm", title, untrusted, "command")
	if err != nil {
		return core.TrustDeny, err
	}
	return a.choice, nil
}

// ConfirmPath implements core.PathConfirmer: a file confirmation that also offers
// to trust the containing directory.
func (w *worker) ConfirmPath(title, dir string) (core.TrustChoice, error) {
	if w.approved() {
		return core.TrustOnce, nil
	}
	a, err := w.prompt("confirm", title, []string{dir}, "path")
	if err != nil {
		return core.TrustDeny, err
	}
	return a.choice, nil
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
	w.latestTool = cloneToolCall(&c)
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
