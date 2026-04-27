package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

type selectModel struct {
	prompt  string
	choices []string
	cursor  int
}

func (m selectModel) Init() tea.Cmd { return nil }

func (m selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.cursor = -1
			return m, tea.Quit

		case "up", "j":
			m.cursor = (m.cursor + 1) % len(m.choices)

		case "down", "k":
			m.cursor = (m.cursor - 1 + len(m.choices)) % len(m.choices)

		case "enter", "space":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m selectModel) View() string {
	s := ""
	for i, choice := range m.choices {
		cursor := " "
		if m.cursor == i {
			cursor = "*"
		}
		s += fmt.Sprintf("[%s] %s\n", cursor, choice)
	}

	return fmt.Sprintf("%s\n%s\n%s\n",
		Styles["ExeAsk"].Render(m.prompt),
		Styles["Choice"].Render(s),
		Styles["Debug"].Render("(Press ↑/↓ and Enter to choose; q or ctrl+c to quit.)"))
}

func GetUserSelected(prompt string, choices []string) (string, error) {
	m := selectModel{
		prompt:  prompt,
		choices: choices,
		cursor:  0,
	}
	p := tea.NewProgram(m)
	m_res, err := p.Run()
	if err != nil {
		return "", err
	}
	_id := m_res.(selectModel).cursor
	if _id < 0 {
		return "None", nil
	}
	return m.choices[_id], nil
}
