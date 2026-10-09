package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Carudy/pai/internal/core"
)

func commandConfirmModel(t *testing.T, untrusted []string) (*appModel, *promptReq) {
	t.Helper()
	m := newAppModel()
	req := promptReq{
		kind:      promptConfirmCommand,
		title:     "Execute this command? (2 commands)",
		untrusted: untrusted,
		reply:     make(chan promptResult, 1),
	}
	mm, _ := m.Update(promptMsg{req: req})
	return mm.(*appModel), &req
}

// A command confirm maps y/s/a/n onto the trust choices; a plain confirm does not.
func TestConfirmCommandKeys(t *testing.T) {
	cases := []struct {
		key  tea.KeyMsg
		want core.TrustChoice
	}{
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")}, core.TrustOnce},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}, core.TrustSession},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}, core.TrustPersist},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}, core.TrustDeny},
		{tea.KeyMsg{Type: tea.KeyEnter}, core.TrustOnce},
		{tea.KeyMsg{Type: tea.KeyEsc}, core.TrustDeny},
	}
	for _, tc := range cases {
		m, req := commandConfirmModel(t, []string{"sudo"})
		mm, _ := m.Update(tc.key)
		_ = mm.(*appModel)
		select {
		case res := <-req.reply:
			if res.choice != tc.want {
				t.Errorf("key %v: choice %v, want %v", tc.key, res.choice, tc.want)
			}
		default:
			t.Fatal("command confirm not answered")
		}
	}
}

// The command modal names the untrusted programs and spells out the trust keys.
func TestConfirmCommandViewListsUntrusted(t *testing.T) {
	m, _ := commandConfirmModel(t, []string{"sudo", "rm"})
	view := m.View()
	for _, want := range []string{"Execute this command?", "not trusted: sudo, rm", "[s] trust session", "[a] always"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() missing %q:\n%s", want, view)
		}
	}
}

// Untrusted command-chain segments are marked in the printed list.
func TestToolCallMarksUntrustedSegments(t *testing.T) {
	out := renderToolCall(core.ToolCall{
		Name:              "execute",
		Target:            "bash",
		Detail:            "ls -la && sudo rm x",
		UntrustedSegments: []int{1},
	})
	if !strings.Contains(out, "2 commands (1 need approval):") {
		t.Errorf("missing approval count:\n%s", out)
	}
	if !strings.Contains(out, "⚠ sudo rm x") {
		t.Errorf("untrusted segment not marked:\n%s", out)
	}
}
