package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/bubbles/textinput"
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
func NewApp(w io.Writer, session string) *App {
	m := newAppModel()
	m.session = session
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

// Observer returns the core.Observer adapter, reusing the line renderer with a
// writer that routes finished lines above the input bar.
func (a *App) Observer() core.Observer { return NewLineObserver(a.out) }

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
	req := promptReq{kind: promptAsk, title: title, reply: make(chan promptResult, 1)}
	res, err := p.ask(req)
	return res.text, err
}

func (p *appPrompter) Confirm(title string) (bool, error) {
	req := promptReq{kind: promptConfirm, title: title, reply: make(chan promptResult, 1)}
	res, err := p.ask(req)
	return res.ok, err
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

// ─── Model ───────────────────────────────────────────────────────────────────

type promptKind int

const (
	promptAsk promptKind = iota
	promptConfirm
)

type promptResult struct {
	text string
	ok   bool
	err  error
}

type promptReq struct {
	kind  promptKind
	title string
	reply chan promptResult
}

type promptMsg struct{ req promptReq }

type appModel struct {
	input   textinput.Model
	out     *programWriter
	ready   chan struct{}
	session string

	pending     *promptReq
	queue       []string
	width       int
	onInterrupt func() bool

	mu  sync.Mutex // guards queue and err
	err error
}

func newAppModel() *appModel {
	ti := textinput.New()
	ti.Prompt = "> "
	ti.Focus()
	return &appModel{input: ti, ready: make(chan struct{})}
}

func (m *appModel) Init() tea.Cmd {
	// The program is now consuming msgs, so buffered writes may be routed to it.
	if m.out != nil {
		m.out.started.Store(true)
	}
	close(m.ready)
	return m.input.Focus()
}

func (m *appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		// textinput panics on a negative Width (e.g. a pty reporting 0 columns).
		if msg.Width > 3 {
			m.input.Width = msg.Width - 3
		}
		return m, nil

	case lineMsg:
		// Committed output is printed above the live region, so it persists in
		// the terminal's scrollback.
		return m, tea.Println(msg.text)

	case promptMsg:
		m.begin(msg.req)
		return m, nil

	case tea.KeyMsg:
		if m.pending != nil && m.pending.kind == promptConfirm {
			return m.confirmKey(msg), nil
		}
		switch msg.Type {
		case tea.KeyCtrlC:
			return m.interruptKey()
		case tea.KeyEsc:
			if m.pending != nil {
				m.answer(promptResult{err: core.ErrAborted})
			} else {
				m.input.SetValue("")
			}
			return m, nil
		case tea.KeyEnter:
			m.submit()
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// begin opens a prompt. The status line above the input already carries the
// prompt text, so the bar itself stays bare rather than repeating it.
func (m *appModel) begin(req promptReq) {
	m.pending = &req
	m.input.SetValue("")
	if req.kind == promptConfirm {
		m.input.Blur()
		return
	}
	m.input.Focus()
}

// confirmKey maps a modal confirmation keypress onto an answer.
func (m *appModel) confirmKey(k tea.KeyMsg) tea.Model {
	switch k.String() {
	case "y", "Y", "enter":
		m.answer(promptResult{ok: true})
	case "n", "N", "esc":
		m.answer(promptResult{ok: false})
	case "ctrl+c":
		m.answer(promptResult{err: core.ErrAborted})
	}
	return m
}

// interruptKey cancels an in-flight step, or an active prompt, or quits.
func (m *appModel) interruptKey() (tea.Model, tea.Cmd) {
	if m.pending != nil {
		m.answer(promptResult{err: core.ErrAborted})
		return m, nil
	}
	if m.onInterrupt != nil && m.onInterrupt() {
		return m, nil
	}
	return m, tea.Quit
}

// submit answers a pending prompt, or queues the text for the next one.
func (m *appModel) submit() {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return
	}
	if m.pending != nil {
		m.answer(promptResult{text: text})
		return
	}
	m.mu.Lock()
	m.queue = append(m.queue, text)
	m.mu.Unlock()
	m.input.SetValue("")
}

// answer resolves the pending prompt and returns the bar to its idle state.
func (m *appModel) answer(res promptResult) {
	if m.pending == nil {
		return
	}
	m.pending.reply <- res
	m.pending = nil
	m.input.SetValue("")
	m.input.Focus()
}

func (m *appModel) View() string {
	// The session label rides the live region so it stays visible in every state.
	label := ""
	if m.session != "" {
		label = RenderStr("Info", "["+m.session+"]") + " "
	}

	if m.pending != nil && m.pending.kind == promptConfirm {
		// Modal: the keys that resolve it are spelled out, since stray keys are
		// deliberately ignored and would otherwise look like an unresponsive UI.
		prompt := RenderStr("Warn", "⚠︎  "+m.pending.title)
		hint := RenderStr("Hint", "[y/enter] run · [n/esc] skip · [ctrl+c] abort")
		return label + prompt + "\n" + hint
	}

	status := RenderStr("Hint", "⏳ PAI is working — type to queue your next instruction")
	if m.pending != nil {
		status = RenderStr("Warn", "🙋 "+m.pending.title)
	}
	queued := ""
	if n := m.queueLen(); n > 0 {
		queued = " " + RenderStr("Subdued", fmt.Sprintf("(%d queued)", n))
	}
	return label + status + queued + "\n" + m.input.View()
}

// ─── Shared state (accessed from both the loop and the UI goroutine) ─────────

func (m *appModel) popQueue() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.queue) == 0 {
		return "", false
	}
	v := m.queue[0]
	m.queue = m.queue[1:]
	return v, true
}

func (m *appModel) queueLen() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.queue)
}

func (m *appModel) setErr(err error) {
	m.mu.Lock()
	m.err = err
	m.mu.Unlock()
}

func (m *appModel) getErr() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// ─── Output routing ──────────────────────────────────────────────────────────

// lineMsg carries one committed output line from the agent to the UI, where it
// is printed above the live region.
type lineMsg struct{ text string }

// programWriter turns a byte stream into complete lines printed above the live
// region. Partial lines are held back so streamed reasoning/output isn't split
// across scrollback entries; call flush to emit a trailing partial line.
//
// Until the program is running (and after it exits), writes fall back to the
// plain writer so early errors stay visible and shutdown can't strand a writer.
type programWriter struct {
	program  *tea.Program
	fallback io.Writer
	started  atomic.Bool

	mu  sync.Mutex
	buf strings.Builder
}

func (w *programWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	s := w.buf.String()
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			break
		}
		w.emit(strings.TrimSuffix(s[:i], "\r"))
		s = s[i+1:]
	}
	w.buf.Reset()
	w.buf.WriteString(s)
	return len(p), nil
}

func (w *programWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() > 0 {
		w.emit(w.buf.String())
		w.buf.Reset()
	}
}

func (w *programWriter) emit(line string) {
	if w.started.Load() && w.program != nil {
		// Send, not Println: Send is a no-op once the program has exited, whereas
		// Println would block forever on a channel nobody reads any more.
		w.program.Send(lineMsg{text: line})
		return
	}
	fmt.Fprintln(w.fallback, line)
}

// markDead stops routing writes to the program once it has exited.
func (w *programWriter) markDead() { w.started.Store(false) }
