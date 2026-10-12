package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Carudy/pai/internal/core"
)

func (m *appModel) View() string {
	// The session label rides the live region so it stays visible in every state.
	label := RenderStr("Session", "["+SessionLabel(m.session)+"]") + " "

	if m.pending != nil && m.pending.kind == promptConfirmCommand {
		out := label + "\n" + RenderStr("Confirm", "  ⚠︎  "+m.pending.title)
		if len(m.pending.untrusted) > 0 {
			what := "not trusted: "
			if m.pending.path {
				what = "outside trusted paths: "
			}
			out += "\n" + RenderStr("Warn", "  "+what+strings.Join(m.pending.untrusted, ", "))
		}
		return out + "\n" + RenderStr("Hint", "  [y] run once · [s] trust session · [a] always · [n] skip · [ctrl+c] abort")
	}

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

	// The streaming preview (reasoning, or command output not yet newline-
	// terminated) sits just above the chrome. The session/status line and the
	// input bar stay pinned to the bottom of the live region, so the stream never
	// splits the two apart.
	out := ""
	if tail := m.tailView(); tail != "" {
		out = tail + "\n"
	}
	out += label + status + queued
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
	case "read", "edit", "write":
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
