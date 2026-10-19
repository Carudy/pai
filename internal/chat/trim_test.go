package chat

import (
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

const (
	actionA = `{"action":"tool","toolname":"execute","payload":"ls","reason":"look"}`
	actionB = `{"action":"tool","toolname":"execute","payload":"pwd","reason":"again"}`
)

// A response that stops at its action is left exactly as it is; everything after the
// action is what a long-context model invents when it imitates the transcript, and it
// is dropped before the turn is ever replayed.
func TestTrimToAction(t *testing.T) {
	cases := []struct {
		name        string
		content     string
		want        string
		wantDropped bool
	}{
		{"a lone object is untouched", actionA, actionA, false},
		{"trailing whitespace is not a tail", actionA + "\n\n", actionA + "\n\n", false},
		{"the imitated transcript goes", actionA + "\n\n[cmd result]\nCOMMAND: pwd\n" + actionB, actionA, true},
		{"a second action goes", actionA + actionB, actionA, true},
		{"prose before the action stays", "Let me check.\n" + actionA + "\n[cmd result]\nok", "Let me check.\n" + actionA, true},
		{"braces inside the payload do not derail it", `{"action":"tool","toolname":"execute","payload":{"cmd":"echo {}","dir":"/x"},"reason":"r"}` + "\ntrailing", `{"action":"tool","toolname":"execute","payload":{"cmd":"echo {}","dir":"/x"},"reason":"r"}`, true},
		{"a first object that is not an action is left alone", `{"note":"hi"}` + "\n" + actionB, `{"note":"hi"}` + "\n" + actionB, false},
		{"prose with stray braces is left alone", "use {a} for maps\n" + actionA + "\ntail", "use {a} for maps\n" + actionA + "\ntail", false},
		{"a truncated response is left alone", `{"action":"tool","payload":"ls"`, `{"action":"tool","payload":"ls"`, false},
		{"prose alone has nothing to trim", "just thinking out loud", "just thinking out loud", false},
		{"no brace at all", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, dropped := TrimToAction(tc.content)
			if got != tc.want {
				t.Errorf("kept = %q, want %q", got, tc.want)
			}
			if (dropped > 0) != tc.wantDropped {
				t.Errorf("dropped = %d, want dropped>0 = %v", dropped, tc.wantDropped)
			}
			if want := len(tc.content) - len(tc.want); dropped != want {
				t.Errorf("dropped = %d, want %d", dropped, want)
			}
		})
	}
}

// Only assistant turns are cut: a tool result is command output, and one that happens
// to contain a JSON object is data, not an action. Nothing to cut means no copy.
func TestTrimHistoryTouchesOnlyAssistantTurns(t *testing.T) {
	result := provider.Message{Role: provider.RoleUser, Kind: core.KindToolResult, Content: "[cmd result]\n" + actionA + "\ntail"}
	input := provider.Message{Role: provider.RoleUser, Kind: core.KindInput, Content: "go"}
	output := provider.Message{Role: provider.RoleAssistant, Kind: core.KindOutput, Content: actionA + "\n[cmd result]\nfake"}

	history := []provider.Message{input, result, output}
	trimmed := TrimHistory(history)
	if trimmed[0].Content != input.Content || trimmed[1].Content != result.Content {
		t.Errorf("a non-assistant turn was rewritten: %q / %q", trimmed[0].Content, trimmed[1].Content)
	}
	if trimmed[2].Content != actionA {
		t.Errorf("assistant turn = %q, want %q", trimmed[2].Content, actionA)
	}
	if history[2].Content != output.Content {
		t.Error("TrimHistory mutated its input")
	}
	if &trimmed[2] == &history[2] {
		t.Error("a trimmed turn must not alias its input element")
	}

	// Nothing to cut: the input slice comes back as it is, so the common case costs no
	// allocation on the request path.
	clean := []provider.Message{input, {Role: provider.RoleAssistant, Kind: core.KindOutput, Content: actionA}}
	if out := TrimHistory(clean); &out[0] != &clean[0] {
		t.Error("nothing to trim should return the input slice")
	}
}

// The log says when a turn over-ran, so it is not discovered later as a mystery; small
// tails are stray characters and stay out of the log.
func TestDescribeTrim(t *testing.T) {
	big := actionA + "\n[cmd result]\nfake\n" + actionB + strings.Repeat(" blah", 60)
	note := DescribeTrim(big)
	for _, want := range []string{"dropped ", "past its action", "1 further action(s)"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q is missing %q", note, want)
		}
	}
	if got := DescribeTrim(actionA + "\nx"); got != "" {
		t.Errorf("a stray character should not be reported: %q", got)
	}
	if got := DescribeTrim(actionA); got != "" {
		t.Errorf("a clean response should not be reported: %q", got)
	}
}
