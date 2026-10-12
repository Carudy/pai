package tui

import (
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Carudy/pai/internal/core"
)

// ─── Model ───────────────────────────────────────────────────────────────────

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
		if m.pending != nil && (m.pending.kind == promptConfirm || m.pending.kind == promptConfirmCommand) {
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
	if req.kind == promptConfirm || req.kind == promptConfirmCommand {
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

// confirmKey maps a modal confirmation keypress onto an answer. A plain confirm
// answers yes/no; a command confirm also maps s/a onto the trust choices.
func (m *appModel) confirmKey(k tea.KeyMsg) tea.Model {
	command := m.pending != nil && m.pending.kind == promptConfirmCommand
	switch key := k.String(); {
	case key == "y" || key == "Y" || key == "enter":
		m.answer(promptResult{ok: true, choice: core.TrustOnce})
	case command && (key == "s" || key == "S"):
		m.answer(promptResult{ok: true, choice: core.TrustSession})
	case command && (key == "a" || key == "A"):
		m.answer(promptResult{ok: true, choice: core.TrustPersist})
	case key == "n" || key == "N" || key == "esc":
		m.answer(promptResult{ok: false, choice: core.TrustDeny})
	case key == "ctrl+c":
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
