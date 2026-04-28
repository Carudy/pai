package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type ChatMessage struct {
	Role    string
	Content string
}

// ChatFunc is a synchronous chat callback (non-streaming).
type ChatFunc func(userInput string) (string, error)

// StreamChatFunc is a streaming chat callback. It receives the user input and a
// token handler. The handler is called with each content token as it arrives.
// The return value is the full response text (same as what was sent via onToken).
type StreamChatFunc func(ctx context.Context, userInput string, onToken func(string)) (string, error)

// reservedLines: title(1) + separator(1) + input(1) + help(1) = 4
const (
	reservedLines = 4
	prefixLen     = 6 // len("[PAI] ") == len("[You] ")
)

type chatModel struct {
	messages     []ChatMessage
	input        string
	cursor       int
	scrollOffset int
	width        int
	height       int
	chatFunc     ChatFunc
	streamFunc   StreamChatFunc
	ctx          context.Context
	quitting     bool
	waiting      bool
	partial      *strings.Builder // pointer to avoid illegal copy panics
	tokenCh      chan string
	errCh        chan error
}

func (m chatModel) Init() tea.Cmd { return nil }

// ── Helpers ──────────────────────────────────────────────────────────────────

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	var result []string
	for _, line := range strings.Split(text, "\n") {
		if len(line) == 0 {
			result = append(result, "")
			continue
		}
		runes := []rune(line)
		for len(runes) > 0 {
			if len(runes) <= width {
				result = append(result, string(runes))
				break
			}
			breakAt := width
			for breakAt > 0 && runes[breakAt] != ' ' {
				breakAt--
			}
			if breakAt == 0 {
				breakAt = width
			}
			result = append(result, string(runes[:breakAt]))
			runes = runes[breakAt:]
			for len(runes) > 0 && runes[0] == ' ' {
				runes = runes[1:]
			}
		}
	}
	return result
}

func renderAllLines(messages []ChatMessage, contentWidth int) []string {
	tagFor := map[string]string{
		"assistant": "[PAI]",
		"user":      "[You]",
	}
	const indent = "      "

	var lines []string
	for _, msg := range messages {
		tag, ok := tagFor[msg.Role]
		if !ok {
			tag = "[   ]"
		}
		wrapped := wrapText(msg.Content, contentWidth)
		for i, l := range wrapped {
			if i == 0 {
				lines = append(lines, tag+" "+l)
			} else {
				lines = append(lines, indent+l)
			}
		}
		lines = append(lines, "")
	}
	return lines
}

func (m chatModel) msgAreaHeight() int {
	h := m.height - reservedLines
	if h < 1 {
		return 1
	}
	return h
}

func (m chatModel) allDisplayLines() []string {
	contentWidth := m.width - prefixLen
	if contentWidth < 10 {
		contentWidth = 10
	}
	lines := renderAllLines(m.messages, contentWidth)

	// Render partial (in-flight) streaming content.
	if m.partial != nil && m.partial.Len() > 0 {
		wrapped := wrapText(m.partial.String(), contentWidth)
		for i, l := range wrapped {
			if i == 0 {
				lines = append(lines, "[PAI] "+l)
			} else {
				lines = append(lines, "      "+l)
			}
		}
		lines = append(lines, "")
	}

	if m.waiting && (m.partial == nil || m.partial.Len() == 0) {
		lines = append(lines, "      ⏳ waiting for response…")
	}
	return lines
}

func (m chatModel) maxScroll() int {
	n := len(m.allDisplayLines()) - m.msgAreaHeight()
	if n < 0 {
		return 0
	}
	return n
}

func (m *chatModel) scrollBy(delta int) {
	m.scrollOffset = clamp(m.scrollOffset+delta, 0, m.maxScroll())
}

// ── Messages ─────────────────────────────────────────────────────────────────

type chatResponseMsg struct {
	response string
	err      error
}

type chatStreamTokenMsg struct {
	token string
}

type chatStreamDoneMsg struct {
	err error
}

// ── Update ───────────────────────────────────────────────────────────────────

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.scrollOffset = clamp(m.scrollOffset, 0, m.maxScroll())

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit

		case "enter":
			if m.waiting {
				break
			}
			trimmed := strings.TrimSpace(m.input)
			if trimmed == "" {
				break
			}
			m.messages = append(m.messages, ChatMessage{Role: "user", Content: trimmed})
			m.input = ""
			m.cursor = 0
			m.scrollOffset = 0
			m.waiting = true
			// Set up streaming channels and start the goroutine.
			m.tokenCh = make(chan string)
			m.errCh = make(chan error, 1)
			m.partial = new(strings.Builder)
			go func() {
				defer close(m.tokenCh)
				_, err := m.streamFunc(m.ctx, trimmed, func(tok string) {
					m.tokenCh <- tok
				})
				m.errCh <- err
			}()
			return m, m.waitForNextToken()

		case "backspace":
			if m.cursor > 0 {
				m.input = m.input[:m.cursor-1] + m.input[m.cursor:]
				m.cursor--
			}

		case "left":
			if m.cursor > 0 {
				m.cursor--
			}

		case "right":
			if m.cursor < len(m.input) {
				m.cursor++
			}

		case "up":
			m.scrollBy(5)

		case "down":
			m.scrollBy(-5)

		case "ctrl+up":
			m.scrollBy(m.msgAreaHeight() / 2)

		case "ctrl+down":
			m.scrollBy(-(m.msgAreaHeight() / 2))

		case "ctrl+l":
			m.scrollOffset = 0

		default:
			if !m.waiting && len(msg.String()) == 1 {
				m.input = m.input[:m.cursor] + msg.String() + m.input[m.cursor:]
				m.cursor++
			}
		}

	case chatResponseMsg:
		m.waiting = false
		if msg.err != nil {
			m.messages = append(m.messages, ChatMessage{
				Role:    "assistant",
				Content: fmt.Sprintf("[error: %v]", msg.err),
			})
		} else {
			m.messages = append(m.messages, ChatMessage{Role: "assistant", Content: msg.response})
		}
		m.scrollOffset = 0

	case chatStreamTokenMsg:
		m.partial.WriteString(msg.token)
		m.scrollOffset = 0
		return m, m.waitForNextToken()

	case chatStreamDoneMsg:
		m.waiting = false
		if msg.err != nil {
			m.messages = append(m.messages, ChatMessage{
				Role:    "assistant",
				Content: fmt.Sprintf("[error: %v]", msg.err),
			})
		} else if m.partial.Len() > 0 {
			m.messages = append(m.messages, ChatMessage{
				Role:    "assistant",
				Content: m.partial.String(),
			})
		}
		m.partial = new(strings.Builder)
		m.tokenCh = nil
		m.errCh = nil
		m.scrollOffset = 0
	}

	return m, nil
}

// ── View ─────────────────────────────────────────────────────────────────────

func (m chatModel) View() string {
	if m.quitting {
		return "Goodbye!\n"
	}

	msgH := m.msgAreaHeight()
	allLines := m.allDisplayLines()

	maxScroll := len(allLines) - msgH
	if maxScroll < 0 {
		maxScroll = 0
	}
	offset := clamp(m.scrollOffset, 0, maxScroll)

	end := len(allLines) - offset
	start := end - msgH
	if start < 0 {
		start = 0
	}
	visible := allLines[start:end]

	var msgBuf strings.Builder
	for i := 0; i < msgH-len(visible); i++ {
		msgBuf.WriteByte('\n')
	}
	for _, l := range visible {
		if len(l) >= 5 && (l[:5] == "[PAI]" || l[:5] == "[You]") {
			tag := l[:5]
			rest := l[5:]
			msgBuf.WriteString(Styles["Speaker"].Render(tag) + Styles["Content"].Render(rest) + "\n")
		} else {
			msgBuf.WriteString(Styles["Content"].Render(l) + "\n")
		}
	}

	titleText := "🤖 Interactive Asking Mode"
	if offset > 0 {
		titleText += fmt.Sprintf("  ↑ +%d lines", offset)
	}
	title := Styles["Title"].Render(titleText)

	sep := strings.Repeat("─", m.width)

	var inputDisplay string
	switch {
	case m.waiting:
		inputDisplay = Styles["Help"].Render("⏳ waiting… (ctrl+c to quit)")
	case m.input == "":
		inputDisplay = Styles["Help"].Render("Ask something… (↑/↓ · ctrl+↑/↓ half-page · ctrl+l bottom · ctrl+c quit)")
	default:
		inputDisplay = m.input[:m.cursor] + "█" + m.input[m.cursor:]
	}

	return fmt.Sprintf("%s\n%s%s\n%s", title, msgBuf.String(), sep, inputDisplay)
}

// ── Cmd helpers ──────────────────────────────────────────────────────────────

// waitForNextToken returns a tea.Cmd that blocks until the next token or
// stream-done signal arrives on m.tokenCh / m.errCh.
func (m chatModel) waitForNextToken() tea.Cmd {
	if m.tokenCh == nil {
		return nil
	}
	return func() tea.Msg {
		tok, ok := <-m.tokenCh
		if ok {
			return chatStreamTokenMsg{token: tok}
		}
		// tokenCh closed → stream finished, drain errCh.
		err := <-m.errCh
		return chatStreamDoneMsg{err: err}
	}
}

func (m chatModel) sendChatCmd(userInput string) tea.Cmd {
	// Legacy blocking path (non-streaming).
	return func() tea.Msg {
		response, err := m.chatFunc(userInput)
		return chatResponseMsg{response: response, err: err}
	}
}

// ── Entry points ─────────────────────────────────────────────────────────────

// StartChat starts a chat TUI with a blocking ChatFunc (non-streaming).
func StartChat(chatFunc ChatFunc, initialMessages []ChatMessage) error {
	p := tea.NewProgram(
		chatModel{messages: initialMessages, chatFunc: chatFunc},
		tea.WithAltScreen(),
	)
	_, err := p.Run()
	return err
}

// StartStreamChat starts a chat TUI with a streaming StreamChatFunc.
// Tokens are rendered incrementally as they arrive.
func StartStreamChat(ctx context.Context, streamFunc StreamChatFunc, initialMessages []ChatMessage) error {
	p := tea.NewProgram(
		chatModel{
			messages:   initialMessages,
			streamFunc: streamFunc,
			ctx:        ctx,
		},
		tea.WithAltScreen(),
	)
	_, err := p.Run()
	return err
}
