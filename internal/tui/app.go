package tui

import (
	"io"
	"os"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Carudy/pai/internal/core"
)

// App is the interactive terminal UI: an inline (non-alt-screen) bubbletea
// program that owns the terminal for the duration of a run and provides the
// core.Observer and core.Prompter adapters.
//
//   - Observer events render as scrollback lines above a one-line input bar, so
//     finished output persists in the terminal's normal history.
//   - Prompter.Ask blocks the caller until the user submits. Text typed while
//     the agent was busy is queued and consumed first, so the user can type the
//     next instruction without waiting for the current step.
//   - Prompter.Confirm is modal: the input bar is disabled until the user
//     answers y/n.
//
// Only use App on a real terminal; see cli for the line-mode fallback.
type App struct {
	program *tea.Program
	model   *appModel
	out     *programWriter

	done chan struct{}
	once sync.Once
}

// NewApp builds an App that renders to w and reads from stdin. session is a
// label for the current session (e.g. its name, or "<temp session>"), shown in
// the live region so the user always knows where the conversation is going.
// When interactive is false the input bar is hidden — a one-shot run has nothing
// to type at, but its confirmations are still better handled here than on a
// plain line prompt.
func NewApp(w io.Writer, session string, interactive bool) *App {
	m := newAppModel()
	m.session = session
	m.interactive = interactive
	out := &programWriter{fallback: w}
	m.out = out
	p := tea.NewProgram(m,
		tea.WithOutput(w),
		tea.WithInput(os.Stdin),
		// The app handles Ctrl+C itself (as a keypress in raw mode); leaving
		// bubbletea's handler on would let a signal tear the UI down mid-step and
		// strand the loop on a write with no reader.
		tea.WithoutSignalHandler(),
	)
	out.program = p
	return &App{program: p, model: m, out: out, done: make(chan struct{})}
}

// Start runs the UI in the background and returns once the terminal is ready to
// receive output. It must be called before the agent loop writes anything.
func (a *App) Start() {
	go func() {
		if _, err := a.program.Run(); err != nil {
			a.model.setErr(err)
		}
		// The renderer is gone; stop routing writes at it.
		a.out.markDead()
		a.once.Do(func() { close(a.done) })
	}()
	// Wait for the terminal, but don't hang if the program died on startup.
	select {
	case <-a.model.ready:
	case <-a.done:
	}
}

// Close flushes buffered output, stops the UI, and unblocks any in-flight
// prompt. It is safe to call more than once, and it never blocks the caller on
// the event loop: after Wait the program is gone, so pending prompts resolve
// through the done channel instead.
func (a *App) Close() {
	a.once.Do(func() {
		a.out.flush()
		a.program.Quit()
		a.program.Wait()
		a.out.markDead()
		close(a.done)
	})
}

// Observer returns the core.Observer adapter. Output goes through the line
// renderer (so finished lines land in scrollback), while session changes also
// update the live region's label.
func (a *App) Observer() core.Observer {
	return &appObserver{LineObserver: NewLineObserver(a.out), app: a}
}

// appObserver renders events above the input bar, and keeps the live region's
// session label in step with the conversation's storage.
type appObserver struct {
	*LineObserver
	app *App
}

func (o *appObserver) Session(name string) {
	o.LineObserver.Session(name)
	o.app.program.Send(sessionMsg{name: name})
}

// ToolCall/ToolResult drive the live "running <cmd> … 42s" indicator, so a long
// silent command is visibly alive rather than indistinguishable from a hang.
func (o *appObserver) ToolCall(c core.ToolCall) {
	o.LineObserver.ToolCall(c)
	o.app.program.Send(toolMsg{label: toolLiveLabel(c)})
}

func (o *appObserver) ToolResult(r core.ToolResult) {
	o.LineObserver.ToolResult(r)
	o.app.program.Send(toolMsg{})
}

// SessionLabel renders a session name for display; an empty name is a run that
// is not persisted.
func SessionLabel(name string) string {
	if name == "" {
		return "<temp session>"
	}
	return name
}

// Writer is where diagnostics should go so they don't corrupt the live region.
func (a *App) Writer() io.Writer { return a.out }

// Prompter returns the core.Prompter adapter.
func (a *App) Prompter() core.Prompter { return &appPrompter{app: a} }

// SetInterrupt registers the callback invoked when Ctrl+C is pressed while a
// step is running. It should report whether a step was actually cancelled.
func (a *App) SetInterrupt(fn func() bool) { a.model.onInterrupt = fn }

// Err reports a fatal UI error, if the program exited abnormally.
func (a *App) Err() error { return a.model.getErr() }

// ─── Prompter ────────────────────────────────────────────────────────────────

type appPrompter struct{ app *App }

func (p *appPrompter) Ask(title string) (string, error) {
	// Queueing: text typed while the agent was busy answers this immediately.
	if v, ok := p.app.model.popQueue(); ok {
		return v, nil
	}
	// A steering message that never reached a step boundary still belongs to the
	// user; deliver it rather than leaving it stranded.
	if v, ok := p.app.model.popSteer(); ok {
		return v, nil
	}
	req := promptReq{kind: promptAsk, title: title, reply: make(chan promptResult, 1)}
	res, err := p.ask(req)
	return res.text, err
}

// Steer implements core.Steerer: a non-blocking poll the loop makes between
// steps to deliver a mid-task message.
func (p *appPrompter) Steer() (string, bool) { return p.app.model.popSteer() }

func (p *appPrompter) Confirm(title string) (bool, error) {
	req := promptReq{kind: promptConfirm, title: title, reply: make(chan promptResult, 1)}
	res, err := p.ask(req)
	return res.ok, err
}

// ConfirmCommand implements core.CommandConfirmer: a command confirmation that can
// also trust the flagged names for this run or for future runs.
func (p *appPrompter) ConfirmCommand(title string, untrusted []string) (core.TrustChoice, error) {
	req := promptReq{kind: promptConfirmCommand, title: title, untrusted: untrusted, reply: make(chan promptResult, 1)}
	res, err := p.ask(req)
	if err != nil {
		return core.TrustDeny, err
	}
	return res.choice, nil
}

// ConfirmPath implements core.PathConfirmer: a file-change confirmation that can
// also trust the containing directory for this run or for future runs. It reuses
// the command modal, showing the directory as the thing to trust.
func (p *appPrompter) ConfirmPath(title, dir string) (core.TrustChoice, error) {
	req := promptReq{kind: promptConfirmCommand, title: title, untrusted: []string{dir}, path: true, reply: make(chan promptResult, 1)}
	res, err := p.ask(req)
	if err != nil {
		return core.TrustDeny, err
	}
	return res.choice, nil
}

func (p *appPrompter) ask(req promptReq) (promptResult, error) {
	p.app.program.Send(promptMsg{req})
	select {
	case res := <-req.reply:
		return res, res.err
	case <-p.app.done:
		// The UI is gone; don't strand the loop on a reply that can't come.
		return promptResult{}, core.ErrAborted
	}
}

type promptKind int

const (
	promptAsk promptKind = iota
	promptConfirm
	promptConfirmCommand
)

type promptResult struct {
	text   string
	ok     bool
	choice core.TrustChoice
	err    error
}

type promptReq struct {
	kind      promptKind
	title     string
	untrusted []string
	path      bool // untrusted names a directory, not command names
	reply     chan promptResult
}

type promptMsg struct{ req promptReq }

// toolMsg reports the start (non-empty label) or end (empty label) of a tool, so
// the live region can show how long it has been running.
//
// tickMsg is the once-a-second refresh that keeps that timer moving; it re-arms
// itself while a tool is running.
type toolMsg struct{ label string }
type tickMsg time.Time
