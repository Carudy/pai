package ui

import (
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ── Messages ──────────────────────────────────────────────────────────────────

type spinnerTickMsg time.Time

// ── Model ─────────────────────────────────────────────────────────────────────

type spinnerModel struct {
	label  string
	emoji  string
	frames []string
	idx    int
}

func (m spinnerModel) Init() tea.Cmd {
	return tickCmd()
}

func (m spinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case spinnerTickMsg:
		m.idx = (m.idx + 1) % len(m.frames)
		return m, tickCmd()
	}
	return m, nil
}

func (m spinnerModel) View() string {
	frame := m.frames[m.idx]
	return fmt.Sprintf("\r%s %s %s", Styles["Help"].Render(frame), m.emoji, Styles["Subdued"].Render(m.label))
}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return spinnerTickMsg(t)
	})
}

// ── Spinner Frame Sets ────────────────────────────────────────────────────────

var defaultFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// ── Public API ────────────────────────────────────────────────────────────────

// ShowSpinner launches an ephemeral spinner. The returned stop function clears
// the spinner line from the terminal and shuts down the spinner goroutine.
//
// Usage:
//
//	stop := ui.ShowSpinner(ctx, "🧠", "Thinking...")
//	defer stop()
//	// ... do work ...
//	stop()
func ShowSpinner(emoji, label string) (stop func()) {
	m := spinnerModel{
		label:  label,
		emoji:  emoji,
		frames: defaultFrames,
	}

	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithoutSignalHandler(), tea.WithOutput(os.Stderr))

	go func() {
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "spinner error: %v\n", err)
		}
	}()

	return func() {
		fmt.Fprint(os.Stderr, "\r\x1b[K")
		p.Quit()
	}
}
