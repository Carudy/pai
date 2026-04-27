package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type inputModel struct {
	prompt string
	value  string
	cursor int
	done   bool
	quit   bool
}

func (m inputModel) Init() tea.Cmd { return nil }

func (m inputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.quit = true
			m.done = true
			return m, tea.Quit

		case "enter":
			m.done = true
			return m, tea.Quit

		case "backspace":
			if m.cursor > 0 {
				m.value = m.value[:m.cursor-1] + m.value[m.cursor:]
				m.cursor--
			}

		case "left":
			if m.cursor > 0 {
				m.cursor--
			}

		case "right":
			if m.cursor < len(m.value) {
				m.cursor++
			}

		default:
			if len(msg.String()) == 1 {
				m.value = m.value[:m.cursor] + msg.String() + m.value[m.cursor:]
				m.cursor++
			}
		}
	}
	return m, nil
}

func (m inputModel) View() string {
	var buf strings.Builder
	buf.WriteString(Styles["Warn"].Render(m.prompt) + "\n")
	if m.value == "" {
		buf.WriteString(Styles["Help"].Render("▊ (type here, enter to confirm, ctrl+c to cancel)") + "\n")
	} else {
		before := m.value[:m.cursor]
		after := m.value[m.cursor:]
		buf.WriteString(before + "█" + after + "\n")
	}
	return buf.String()
}

// GetUserTextInput opens a simple one-line text input bubble.
// Returns the entered string, or ("", nil) if the user cancelled.
func GetUserTextInput(prompt string) (string, error) {
	m := inputModel{prompt: prompt}
	p := tea.NewProgram(m)
	res, err := p.Run()
	if err != nil {
		return "", err
	}
	model := res.(inputModel)
	if model.quit {
		return "", nil // cancelled
	}
	return model.value, nil
}
