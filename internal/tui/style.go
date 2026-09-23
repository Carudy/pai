package tui

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
	// Hint is for actionable guidance (key bindings, status). A brighter cousin
	// of the accent huh used (#7571F9), chosen for contrast on dark terminals —
	// the old grey was hard to read.
	"Hint":    lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")),
	"Subdued": lipgloss.NewStyle().Foreground(lipgloss.Color("#ABFAE1")),
	"Content": lipgloss.NewStyle().Foreground(lipgloss.Color("#ddeeff")),

	// Confirm is the modal yes/no prompt. It asks for a go/no-go before an
	// action runs, so it is a distinct, urgent red — deliberately not the purple
	// accent used for hints — and bold to stand out from scrolling output.
	"Confirm": lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F5F")).Bold(true),
	// Session labels the live region. It is an identity marker rather than a
	// status, so a cool cyan sets it apart from the purple hints, green tool
	// tags and amber warnings.
	"Session": lipgloss.NewStyle().Foreground(lipgloss.Color("#7DCFFF")).Bold(true),

	// ── Logging ───────────────────────────────────────────────────────
	"Debug": lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true),
	"Error": lipgloss.NewStyle().Foreground(lipgloss.Red).Bold(true),

	// ── Command highlighting ─────────────────────────────────────────
	// Operators are bold because in a chain they decide what still runs when an
	// earlier command fails — the part worth noticing before approving.
	"CmdString":   lipgloss.NewStyle().Foreground(lipgloss.Color("114")),
	"CmdOperator": lipgloss.NewStyle().Foreground(lipgloss.Color("209")).Bold(true),
	"CmdFlag":     lipgloss.NewStyle().Foreground(lipgloss.Color("75")),
	"CmdVar":      lipgloss.NewStyle().Foreground(lipgloss.Color("180")),
	"CmdComment":  lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Italic(true),

	// ── Edit diff ────────────────────────────────────────────────────
	// Added/removed lines use the conventional green/red; the hunk header is a
	// cool blue so it reads as a location marker, not more changed text.
	"DiffAdd":  lipgloss.NewStyle().Foreground(lipgloss.Color("114")),
	"DiffDel":  lipgloss.NewStyle().Foreground(lipgloss.Color("203")),
	"DiffHunk": lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true),

	// ── Decor ─────────────────────────────────────────────────────────
	"Separator": lipgloss.NewStyle().Foreground(lipgloss.Color("236")),

	// ── Tags ──────────────────────────────────────────────────────────
	"TagSystem": lipgloss.NewStyle().Foreground(lipgloss.Color("243")),
	"TagAgent":  lipgloss.NewStyle().Foreground(lipgloss.Color("78")).Bold(true),
	"TagExec":   lipgloss.NewStyle().Foreground(lipgloss.Color("#05AB6A")).Bold(true),
	"TagUser":   lipgloss.NewStyle().Foreground(lipgloss.Color("147")).Bold(true),

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
