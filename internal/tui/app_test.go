package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Carudy/pai/internal/core"
)

// typeString feeds s to the model as if typed, then presses Enter.
func typeAndEnter(m *appModel, s string) *appModel {
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	m = mm.(*appModel)
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return mm.(*appModel)
}

func press(m *appModel, kt tea.KeyType) *appModel {
	mm, _ := m.Update(tea.KeyMsg{Type: kt})
	return mm.(*appModel)
}

// Typing while no prompt is pending must queue, not be lost.
func TestTypingWhileBusyQueues(t *testing.T) {
	m := typeAndEnter(newAppModel(), "next task")
	if got := m.queueLen(); got != 1 {
		t.Fatalf("queueLen = %d, want 1", got)
	}
	if v := m.input.Value(); v != "" {
		t.Errorf("input not cleared after queueing: %q", v)
	}
}

// An empty Enter must not queue anything (avoids accidental instructions).
func TestEmptySubmitIsIgnored(t *testing.T) {
	m := typeAndEnter(newAppModel(), "   ")
	if got := m.queueLen(); got != 0 {
		t.Fatalf("queueLen = %d, want 0", got)
	}
}

// Ask must consume a queued instruction without waiting for the user.
func TestAskConsumesQueueFirst(t *testing.T) {
	m := typeAndEnter(newAppModel(), "queued instruction")
	p := &appPrompter{app: &App{model: m}}

	got, err := p.Ask("Input:")
	if err != nil {
		t.Fatalf("Ask error: %v", err)
	}
	if got != "queued instruction" {
		t.Errorf("Ask = %q, want %q", got, "queued instruction")
	}
	if m.queueLen() != 0 {
		t.Errorf("queue not drained: %d left", m.queueLen())
	}
}

// Submitting while an Ask is pending must answer it, not queue.
func TestSubmitAnswersPendingAsk(t *testing.T) {
	m := newAppModel()
	req := promptReq{kind: promptAsk, title: "Input:", reply: make(chan promptResult, 1)}
	mm, _ := m.Update(promptMsg{req: req})
	m = mm.(*appModel)

	typeAndEnter(m, "hello")

	select {
	case res := <-req.reply:
		if res.err != nil {
			t.Fatalf("unexpected error: %v", res.err)
		}
		if res.text != "hello" {
			t.Errorf("answer = %q, want %q", res.text, "hello")
		}
	default:
		t.Fatal("pending Ask was not answered")
	}
	if m.queueLen() != 0 {
		t.Errorf("answer leaked into the queue: %d", m.queueLen())
	}
}

// Esc on a pending Ask aborts it with core.ErrAborted (ends the session).
func TestEscAbortsPendingAsk(t *testing.T) {
	m := newAppModel()
	req := promptReq{kind: promptAsk, title: "Input:", reply: make(chan promptResult, 1)}
	mm, _ := m.Update(promptMsg{req: req})
	m = mm.(*appModel)

	press(m, tea.KeyEsc)

	select {
	case res := <-req.reply:
		if res.err != core.ErrAborted {
			t.Errorf("err = %v, want core.ErrAborted", res.err)
		}
	default:
		t.Fatal("pending Ask was not aborted")
	}
}

// A confirm is modal: keys resolve it and never touch the queue.
func TestConfirmKeys(t *testing.T) {
	cases := []struct {
		name    string
		key     tea.KeyMsg
		wantOK  bool
		aborted bool
	}{
		{"y", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")}, true, false},
		{"n", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}, false, false},
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}, true, false},
		{"esc", tea.KeyMsg{Type: tea.KeyEsc}, false, false},
		{"ctrl+c", tea.KeyMsg{Type: tea.KeyCtrlC}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newAppModel()
			req := promptReq{kind: promptConfirm, title: "Execute this command?", reply: make(chan promptResult, 1)}
			mm, _ := m.Update(promptMsg{req: req})
			m = mm.(*appModel)

			mm, _ = m.Update(tc.key)
			m = mm.(*appModel)

			select {
			case res := <-req.reply:
				if tc.aborted {
					if res.err != core.ErrAborted {
						t.Errorf("err = %v, want core.ErrAborted", res.err)
					}
				} else if res.err != nil {
					t.Errorf("unexpected err: %v", res.err)
				} else if res.ok != tc.wantOK {
					t.Errorf("ok = %v, want %v", res.ok, tc.wantOK)
				}
			default:
				t.Fatal("confirm not answered")
			}
			if m.queueLen() != 0 {
				t.Errorf("confirm leaked into the queue: %d", m.queueLen())
			}
		})
	}
}

// Keys outside the map are ignored: the prompt stays open and unanswered.
func TestConfirmIgnoresOtherKeys(t *testing.T) {
	m := newAppModel()
	req := promptReq{kind: promptConfirm, title: "Execute this command?", reply: make(chan promptResult, 1)}
	mm, _ := m.Update(promptMsg{req: req})
	m = mm.(*appModel)

	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("x")},
		{Type: tea.KeyUp},
		{Type: tea.KeyTab},
		{Type: tea.KeyBackspace},
	} {
		mm, _ = m.Update(k)
		m = mm.(*appModel)
	}

	select {
	case res := <-req.reply:
		t.Fatalf("ignored key answered the prompt: %+v", res)
	default:
	}
	if m.pending == nil {
		t.Fatal("prompt should still be open")
	}
}

// The modal spells out the accepted keys so a rejected key doesn't look broken.
func TestConfirmViewShowsKeyHints(t *testing.T) {
	m := newAppModel()
	req := promptReq{kind: promptConfirm, title: "Execute this command?", reply: make(chan promptResult, 1)}
	mm, _ := m.Update(promptMsg{req: req})
	m = mm.(*appModel)

	view := m.View()
	for _, want := range []string{
		"Execute this command?",
		"[y/enter] run",
		"[n/esc] skip",
		"[ctrl+c] abort",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("View() missing %q:\n%s", want, view)
		}
	}
}

// The session label is part of the live region, so it shows in every state.
func TestViewShowsSessionLabel(t *testing.T) {
	m := newAppModel()
	m.session = "work"

	if !strings.Contains(m.View(), "[work]") {
		t.Errorf("idle View() missing session label:\n%s", m.View())
	}

	req := promptReq{kind: promptConfirm, title: "Execute this command?", reply: make(chan promptResult, 1)}
	mm, _ := m.Update(promptMsg{req: req})
	m = mm.(*appModel)

	if !strings.Contains(m.View(), "[work]") {
		t.Errorf("confirm View() missing session label:\n%s", m.View())
	}
}

// An unpersisted run is labelled as temporary rather than showing nothing.
func TestViewShowsTempSessionLabel(t *testing.T) {
	m := newAppModel()
	m.session = "<temp session>"
	if !strings.Contains(m.View(), "<temp session>") {
		t.Errorf("View() missing temp marker:\n%s", m.View())
	}
}

// Ctrl+C with no prompt pending asks the host to cancel the running step.
func TestCtrlCDuringStepInterrupts(t *testing.T) {
	m := newAppModel()
	called := 0
	m.onInterrupt = func() bool { called++; return true }

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if called != 1 {
		t.Errorf("onInterrupt called %d times, want 1", called)
	}
	if cmd != nil {
		t.Error("an handled interrupt should not also quit")
	}
}

// Ctrl+C with nothing running falls through to quitting.
func TestCtrlCIdleQuits(t *testing.T) {
	m := newAppModel()
	m.onInterrupt = func() bool { return false }

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", cmd())
	}
}
