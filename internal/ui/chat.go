package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type ChatMessage struct {
	Role    string
	Content string
}

type ChatFunc func(userInput string) (string, error)

// reservedLines: title(1) + separator(1) + input(1) + help(1) = 4
// The message area fills everything else.
const (
	reservedLines = 4
	prefixLen     = 6 // len("[PAI] ") == len("[You] ")
)

type chatModel struct {
	messages     []ChatMessage
	input        string
	cursor       int
	scrollOffset int // display lines scrolled up from the bottom; 0 = newest visible
	width        int
	height       int
	chatFunc     ChatFunc
	quitting     bool
	waiting      bool
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

// wrapText splits text into display lines no wider than width runes,
// honouring existing newlines and preferring word boundaries.
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
				breakAt = width // hard-break: no space found
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

// renderAllLines converts messages into wrapped display lines ready for slicing.
func renderAllLines(messages []ChatMessage, contentWidth int) []string {
	tagFor := map[string]string{
		"assistant": "[PAI]",
		"user":      "[You]",
	}
	const indent = "      " // 6 spaces — aligns with "[PAI] "

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
		lines = append(lines, "") // blank separator between messages
	}
	return lines
}

// msgAreaHeight returns the number of lines available for chat content.
func (m chatModel) msgAreaHeight() int {
	h := m.height - reservedLines
	if h < 1 {
		return 1
	}
	return h
}

// allDisplayLines returns the full rendered line slice (including waiting indicator).
func (m chatModel) allDisplayLines() []string {
	contentWidth := m.width - prefixLen
	if contentWidth < 10 {
		contentWidth = 10
	}
	lines := renderAllLines(m.messages, contentWidth)
	if m.waiting {
		lines = append(lines, "      ⏳ waiting for response…")
	}
	return lines
}

// maxScroll returns the maximum valid scrollOffset for the current state.
func (m chatModel) maxScroll() int {
	n := len(m.allDisplayLines()) - m.msgAreaHeight()
	if n < 0 {
		return 0
	}
	return n
}

// scrollBy adjusts scrollOffset by delta lines, clamped to valid range.
func (m *chatModel) scrollBy(delta int) {
	m.scrollOffset = clamp(m.scrollOffset+delta, 0, m.maxScroll())
}

// ── Update ───────────────────────────────────────────────────────────────────

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Re-clamp after resize — terminal shrink can leave offset out of range.
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
			return m, m.sendChatCmd(trimmed)

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
		m.scrollOffset = 0 // snap to bottom on new message

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

	// Final clamp (View may be called before the first WindowSizeMsg).
	maxScroll := len(allLines) - msgH
	if maxScroll < 0 {
		maxScroll = 0
	}
	offset := clamp(m.scrollOffset, 0, maxScroll)

	// Slice the visible window: offset=0 → newest lines at bottom.
	end := len(allLines) - offset
	start := end - msgH
	if start < 0 {
		start = 0
	}
	visible := allLines[start:end]

	// Render message area, padding the top when content is short.
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

	// Title — show scroll hint when not at the bottom.
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

	// Layout: title + msgArea (already \n-terminated) + sep + input
	// = reservedLines(4) + msgH lines total — matches terminal height exactly.
	return fmt.Sprintf("%s\n%s%s\n%s", title, msgBuf.String(), sep, inputDisplay)
}

// ── Cmd ──────────────────────────────────────────────────────────────────────

type chatResponseMsg struct {
	response string
	err      error
}

func (m chatModel) sendChatCmd(userInput string) tea.Cmd {
	return func() tea.Msg {
		response, err := m.chatFunc(userInput)
		return chatResponseMsg{response: response, err: err}
	}
}

// ── Entry point ───────────────────────────────────────────────────────────────

func StartChat(chatFunc ChatFunc, initialMessages []ChatMessage) error {
	p := tea.NewProgram(
		chatModel{messages: initialMessages, chatFunc: chatFunc},
		tea.WithAltScreen(),
	)
	_, err := p.Run()
	return err
}
