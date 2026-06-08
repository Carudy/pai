package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

var Styles = map[string]lipgloss.Style{
	// ── Semantic ──────────────────────────────────────────────────────
	"Success": lipgloss.NewStyle().Foreground(lipgloss.Green),
	"Info":    lipgloss.NewStyle().Foreground(lipgloss.Color("39")),
	"Warn":    lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
	"Help":    lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true),
	"Subdued": lipgloss.NewStyle().Foreground(lipgloss.Color("#ABFAE1")),
	"Cmd":     lipgloss.NewStyle().Foreground(lipgloss.Yellow).Bold(true),
	"Content": lipgloss.NewStyle().Foreground(lipgloss.Color("#ddeeff")),

	// ── Logging ───────────────────────────────────────────────────────
	"Debug": lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true),
	"Error": lipgloss.NewStyle().Foreground(lipgloss.Red).Bold(true),

	// ── Decor ─────────────────────────────────────────────────────────
	"Separator": lipgloss.NewStyle().Foreground(lipgloss.Color("236")),

	// ── Tags ──────────────────────────────────────────────────────────
	"TagSystem": lipgloss.NewStyle().Foreground(lipgloss.Color("243")),
	"TagAgent":  lipgloss.NewStyle().Foreground(lipgloss.Color("78")).Bold(true),
	"TagExec":   lipgloss.NewStyle().Foreground(lipgloss.Color("#05AB6A")).Bold(true),
	"TagUser":   lipgloss.NewStyle().Foreground(lipgloss.Color("147")).Bold(true),
	"TagResult": lipgloss.NewStyle().Foreground(lipgloss.Color("222")).Bold(true),

	// ── Reasoning / thinking ──────────────────────────────────────────
	"Reasoning": lipgloss.NewStyle().Foreground(lipgloss.Color("172")).Italic(true),
	"Thinking":  lipgloss.NewStyle().Foreground(lipgloss.Color("#8E857A")).Italic(true),

	// ── Misc indicators ───────────────────────────────────────────────
	"Token":   lipgloss.NewStyle().Foreground(lipgloss.Color("#5C6370")).Italic(true).Faint(true),
	"Trusted": lipgloss.NewStyle().Foreground(lipgloss.Color("#4A8C6F")).Italic(true),
}

func RenderStr(style, s string) string {
	var b strings.Builder
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		if line != "" {
			b.WriteString(Styles[style].Render(line))
		}
	}
	return b.String()
}
