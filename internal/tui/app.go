package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

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

// toolMsg reports the start (non-empty label) or end (empty label) of a tool, so
// the live region can show how long it has been running.
//
// tickMsg is the once-a-second refresh that keeps that timer moving; it re-arms
// itself while a tool is running.
type toolMsg struct{ label string }
type tickMsg time.Time

type appModel struct {
	input   textinput.Model
	out     *programWriter
	ready   chan struct{}
	session string
	tail    string

	interactive bool
	pending     *promptReq
	queue       []string
	steerQ      []string
	width       int
	onInterrupt func() bool

	toolLabel string
	toolSince time.Time

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
		m.out.markStarted()
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

	case sessionMsg:
		m.session = msg.name
		return m, nil

	case tailMsg:
		// The in-progress line renders inside the live region, so streaming is
		// visible before its newline arrives.
		m.tail = msg.text
		return m, nil

	case toolMsg:
		m.toolLabel = msg.label
		if msg.label == "" {
			m.toolSince = time.Time{}
			return m, nil
		}
		m.toolSince = time.Now()
		return m, tickCmd()

	case tickMsg:
		// Re-render each second while a tool runs; the command re-arms itself.
		if m.toolLabel == "" {
			return m, nil
		}
		return m, tickCmd()

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
		case tea.KeyCtrlG:
			m.steer()
			return m, nil
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
		// A confirmation is modal and must never consume typed-ahead text.
		m.input.Blur()
		return
	}
	// Text typed while PAI was busy is queued; if it is still queued now, it
	// answers this prompt. Otherwise it would sit there until a later prompt,
	// which made an instruction typed in the gap (a first /quit, say) look
	// ignored.
	if v, ok := m.popQueue(); ok {
		m.answer(promptResult{text: v})
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
		// Immediate: text already typed is delivered the moment the step is
		// cancelled, so Ctrl+C reads as "stop this and do what I just said".
		if text := strings.TrimSpace(m.input.Value()); text != "" {
			m.pushQueue(text)
			m.input.SetValue("")
		}
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

// steer queues the typed text for delivery at the next step boundary, so it can
// redirect the agent part-way through a job instead of waiting for it to stop.
func (m *appModel) steer() {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return
	}
	if m.pending != nil {
		m.answer(promptResult{text: text})
		return
	}
	m.mu.Lock()
	m.steerQ = append(m.steerQ, text)
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
	label := RenderStr("Session", "["+SessionLabel(m.session)+"]") + " "

	if m.pending != nil && m.pending.kind == promptConfirm {
		// Modal: the keys that resolve it are spelled out, since stray keys are
		// deliberately ignored and would otherwise look like an unresponsive UI.
		// The prompt is indented onto its own line so an approval reads as a
		// distinct, deliberate step rather than more scrolling output.
		prompt := RenderStr("Confirm", "  ⚠︎  "+m.pending.title)
		hint := RenderStr("Hint", "  [y/enter] run · [n/esc] skip · [ctrl+c] abort")
		return label + "\n" + prompt + "\n" + hint
	}

	status := RenderStr("Hint", "⏳ working — enter queue · ctrl+g steer · ctrl+c stop & send")
	if m.toolLabel != "" {
		status = RenderStr("Warn", fmt.Sprintf("⏳ running %s… %s (ctrl+c to stop)",
			m.toolLabel, formatElapsed(time.Since(m.toolSince))))
	}
	if m.pending != nil {
		// 💬 for the input bar: PAI is waiting for you, not raising a hand to ask.
		status = RenderStr("Warn", "💬 "+m.pending.title)
	}
	queued := ""
	if n, s := m.queueLen(), m.steerLen(); n > 0 || s > 0 {
		queued = " " + RenderStr("Subdued", fmt.Sprintf("(%d queued, %d to steer)", n, s))
	}

	out := label + status + queued
	if tail := m.tailView(); tail != "" {
		out += "\n" + tail
	}
	if m.interactive {
		out += "\n" + m.input.View()
	}
	return out
}

// tailView renders the in-progress line — streaming reasoning or command output
// that has not been newline-terminated yet — truncated to the terminal width,
// keeping its newest end. The text arrives already styled by the observer, so
// the truncation has to be ANSI-aware.
func (m *appModel) tailView() string {
	if m.tail == "" {
		return ""
	}
	s := m.tail
	width := m.width
	if width <= 4 {
		width = 80
	}
	if w := ansi.StringWidth(s); w > width {
		s = ansi.TruncateLeft(s, w-width, "…")
	}
	return "  " + s
}

// tickCmd schedules the next liveness refresh.
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// formatElapsed renders a duration as a compact clock (12s, 2m14s, 1h03m).
func formatElapsed(d time.Duration) string {
	s := int(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
	}
}

// toolLiveLabel is the short description shown while a tool runs: what it is
// doing is more useful than which tool it is.
func toolLiveLabel(c core.ToolCall) string {
	switch c.Name {
	case "execute":
		return clipLine(c.Detail, 48)
	case "remote":
		return "@" + c.Target
	case "websearch":
		return clipLine(c.Detail, 48)
	case "read", "edit":
		return clipLine(c.Target, 48)
	default:
		return c.Name
	}
}

// clipLine flattens s to one line and caps it at n runes.
func clipLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// ─── Shared state (accessed from both the loop and the UI goroutine) ────────

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

func (m *appModel) steerLen() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.steerQ)
}

func (m *appModel) popSteer() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.steerQ) == 0 {
		return "", false
	}
	v := m.steerQ[0]
	m.steerQ = m.steerQ[1:]
	return v, true
}

// pushQueue puts text at the front of the queue, so it is delivered next.
func (m *appModel) pushQueue(text string) {
	m.mu.Lock()
	m.queue = append([]string{text}, m.queue...)
	m.mu.Unlock()
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

// sessionMsg updates the session label shown in the live region.
type sessionMsg struct{ name string }

// tailMsg carries the line currently being written (no newline yet) so it can
// render inside the live region instead of waiting for the line to finish.
type tailMsg struct{ text string }

// tailInterval rate-limits live-tail refreshes. Command output arrives in very
// many small chunks and every refresh is a round-trip to the UI, so the tail is
// allowed to lag by at most a frame's worth of updates.
const tailInterval = 33 * time.Millisecond

// programWriter turns a byte stream into complete lines printed above the live
// region, plus a live tail for the partial line still being written.
//
// Until the program is running (and after it exits), writes fall back to the
// plain writer so early errors stay visible and shutdown can't strand a writer.
type programWriter struct {
	program  *tea.Program
	fallback io.Writer
	started  atomic.Bool

	mu     sync.Mutex
	carry  string    // bytes after the last newline
	tail   string    // most recent tail text handed to the UI
	tailAt time.Time // when that happened
}

func (w *programWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	lines, rest := splitLines(w.carry, string(p))
	w.carry = rest
	for _, ln := range lines {
		w.emitLine(ln)
	}
	// Finishing a line clears the tail at once; otherwise the update is coalesced.
	w.pushTail(rest, len(lines) > 0)
	return len(p), nil
}

func (w *programWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.carry != "" {
		w.emitLine(w.carry)
		w.carry = ""
	}
	w.pushTail("", true)
}

// splitLines returns the complete lines in carry+chunk plus the new carry. A
// carriage return restarts the line, so progress output ("10%\r20%\n") keeps
// only its final state rather than a transcript of every frame.
func splitLines(carry, chunk string) (lines []string, rest string) {
	s := carry + chunk
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			break
		}
		lines = append(lines, lastSegment(s[:i]))
		s = s[i+1:]
	}
	return lines, lastSegment(s)
}

// lastSegment returns a line's visible part: carriage return moves the cursor
// back to column 0, so only what follows the final one is ever displayed. A
// single trailing CR is the CRLF line ending and is simply dropped.
func lastSegment(line string) string {
	line = strings.TrimSuffix(line, "\r")
	if j := strings.LastIndexByte(line, '\r'); j >= 0 {
		return line[j+1:]
	}
	return line
}

func (w *programWriter) emitLine(line string) {
	if w.started.Load() && w.program != nil {
		// Send, not Println: Send is a no-op once the program has exited, whereas
		// Println would block forever on a channel nobody reads any more.
		w.program.Send(lineMsg{text: line})
		return
	}
	fmt.Fprintln(w.fallback, line)
}

// pushTail refreshes the live tail. force skips the rate limit, which is used
// when a line just completed so the tail clears immediately.
func (w *programWriter) pushTail(text string, force bool) {
	if text == w.tail && !force {
		return
	}
	if !force && text != "" && time.Since(w.tailAt) < tailInterval {
		return
	}
	w.tail = text
	w.tailAt = time.Now()
	if w.started.Load() && w.program != nil {
		w.program.Send(tailMsg{text: text})
	}
}

// markStarted enables UI routing. It forgets the remembered tail so the next
// update renders even if its text is unchanged from before startup.
func (w *programWriter) markStarted() {
	w.mu.Lock()
	w.tail = ""
	w.mu.Unlock()
	w.started.Store(true)
}

// markDead stops routing writes to the program once it has exited.
func (w *programWriter) markDead() { w.started.Store(false) }
