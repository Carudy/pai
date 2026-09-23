package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// PAI waiting for your next instruction is not the agent raising a hand to ask
// a question, so the input prompt uses a speech bubble rather than 🙋 (which is
// reserved for a real "ask" action).
func TestInputPromptUsesSpeechBubble(t *testing.T) {
	m := newAppModel()
	m.interactive = true
	req := promptReq{kind: promptAsk, title: "Input:", reply: make(chan promptResult, 1)}
	mm, _ := m.Update(promptMsg{req: req})
	m = mm.(*appModel)

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "💬 Input:") {
		t.Errorf("input prompt should use a speech bubble:\n%s", view)
	}
	if strings.Contains(view, "🙋") {
		t.Errorf("input prompt must not use the agent-asking emoji:\n%s", view)
	}
}

// The line-mode "awaiting input" line matches the same bubble.
func TestAwaitingUsesSpeechBubble(t *testing.T) {
	var b strings.Builder
	NewLineObserver(&b).Awaiting()
	got := ansi.Strip(b.String())
	if !strings.Contains(got, "[PAI 💬]") {
		t.Errorf("awaiting line = %q", got)
	}
}
