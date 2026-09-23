package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The confirm prompt must be a distinct, urgent style — not the purple accent
// used for hints — and it must be visually indented onto its own line.
func TestConfirmPromptIsIndentedAndStyled(t *testing.T) {
	m := newAppModel()
	m.interactive = true
	req := promptReq{kind: promptConfirm, title: "Execute this command?", reply: make(chan promptResult, 1)}
	mm, _ := m.Update(promptMsg{req: req})
	m = mm.(*appModel)

	plain := ansi.Strip(m.View())
	var promptLine string
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "Execute this command?") {
			promptLine = line
		}
	}
	if promptLine == "" {
		t.Fatalf("confirm prompt not rendered:\n%s", plain)
	}
	if !strings.HasPrefix(promptLine, "  ") {
		t.Errorf("confirm prompt is not indented: %q", promptLine)
	}

	// The confirm and hint styles must be applied (ANSI), and differ.
	if got := RenderStr("Confirm", "x"); got == "x" {
		t.Error("Confirm style is not applied")
	}
	if RenderStr("Confirm", "x") == RenderStr("Hint", "x") {
		t.Error("Confirm must look different from Hint")
	}
}

// The session label is its own identity style, not the generic Info colour.
func TestSessionLabelIsStyled(t *testing.T) {
	m := newAppModel()
	m.session = "work"

	if got := RenderStr("Session", "[work]"); got == "[work]" {
		t.Error("Session style is not applied")
	}
	if RenderStr("Session", "x") == RenderStr("Info", "x") {
		t.Error("Session should differ from Info")
	}
	if !strings.Contains(ansi.Strip(m.View()), "[work]") {
		t.Errorf("session label missing from the view:\n%s", m.View())
	}
}
