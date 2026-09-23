package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Carudy/pai/internal/core"
)

var _ core.Steerer = (*appPrompter)(nil)

func typeAndKey(m *appModel, s string, kt tea.KeyType) *appModel {
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	m = mm.(*appModel)
	mm, _ = m.Update(tea.KeyMsg{Type: kt})
	return mm.(*appModel)
}

func TestCtrlGQueuesASteerMessage(t *testing.T) {
	m := typeAndKey(newAppModel(), "wait, check X first", tea.KeyCtrlG)

	if m.steerLen() != 1 {
		t.Fatalf("steerLen = %d, want 1", m.steerLen())
	}
	if m.queueLen() != 0 {
		t.Errorf("a steer must not land in the normal queue: %d", m.queueLen())
	}
	if m.input.Value() != "" {
		t.Errorf("input not cleared: %q", m.input.Value())
	}
}

func TestSteerPops(t *testing.T) {
	m := typeAndKey(newAppModel(), "steer me", tea.KeyCtrlG)
	p := &appPrompter{app: &App{model: m}}

	got, ok := p.Steer()
	if !ok || got != "steer me" {
		t.Fatalf("Steer = (%q, %v), want (steer me, true)", got, ok)
	}
	if _, ok := p.Steer(); ok {
		t.Error("steer queue not drained")
	}
}

// A steering message the loop never reached a boundary for still belongs to the
// user; Ask must deliver it rather than strand it.
func TestAskFallsBackToSteer(t *testing.T) {
	m := typeAndKey(newAppModel(), "leftover steer", tea.KeyCtrlG)
	p := &appPrompter{app: &App{model: m}}

	got, err := p.Ask("Input:")
	if err != nil || got != "leftover steer" {
		t.Fatalf("Ask = (%q, %v), want the steering message", got, err)
	}
}

// Ctrl+C with text typed delivers it as soon as the step is cancelled.
func TestCtrlCWithTypedTextPushesImmediate(t *testing.T) {
	m := newAppModel()
	m.onInterrupt = func() bool { return true }
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("stop and do this")})
	m = mm.(*appModel)

	m = press(m, tea.KeyCtrlC)

	if m.queueLen() != 1 {
		t.Fatalf("queueLen = %d, want the typed text queued for immediate delivery", m.queueLen())
	}
	if v, _ := m.popQueue(); v != "stop and do this" {
		t.Errorf("queued %q", v)
	}
	if m.input.Value() != "" {
		t.Errorf("input not cleared: %q", m.input.Value())
	}
}

// With nothing typed, Ctrl+C still just interrupts.
func TestCtrlCWithoutTextJustInterrupts(t *testing.T) {
	m := newAppModel()
	m.onInterrupt = func() bool { return true }
	m = press(m, tea.KeyCtrlC)
	if m.queueLen() != 0 {
		t.Errorf("queueLen = %d, want 0", m.queueLen())
	}
}
