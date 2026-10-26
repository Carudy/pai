package chat

import (
	"strings"
	"testing"
)

// chatGuide is the exact contract shipped before action sets were introduced.
// The head is prefix-cached, so this byte sequence must not drift.
const chatGuide = `## Response format

Reply with exactly ONE JSON object and nothing else — no prose before or after
it, no markdown code fences. Every response has three fields:
  "action"  one of tool|ask|done|terminate
  "payload" the action's content
  "reason"  a short explanation of why you chose this action

Concrete examples per action:

- tool — run a tool. "toolname" sits at the top level beside "payload"; payload
  holds the tool's arguments, a string or an object per that tool's schema (listed
  above). For example, execute takes a plain command, or an object naming the
  directory to run it in:
  {"action":"tool","toolname":"execute","payload":"df -h","reason":"check disk space"}
  {"action":"tool","toolname":"execute","payload":{"cmd":"ls -la","dir":"/var/log"},"reason":"inspect the nginx log dir"}

- ask — ask the user a question; payload is the question string.
  {"action":"ask","payload":"Which host should I deploy to?","reason":"the target host is unclear"}

- done — the task is finished; payload is a summary of what you accomplished, use markdown format if suitable.
  {"action":"done","payload":"Deployed v1.2 to staging; the health check returned 200.","reason":"task complete"}

- terminate — the task cannot be completed; payload explains why.
  {"action":"terminate","payload":"No SSH access to the host and no credentials were provided.","reason":"cannot proceed"}

Every { needs a matching }; never emit more than one object.`

func TestChatGuideIsByteStable(t *testing.T) {
	if got := OutputGuide(); got != chatGuide {
		t.Fatalf("chat guide changed:\n--- got ---\n%s\n--- want ---\n%s", got, chatGuide)
	}
}

func TestServiceGuideAdvertisesServiceActions(t *testing.T) {
	guide := OutputGuideFor(ServiceActions())
	for _, want := range []string{
		`"action"  one of tool|ask|inform|yield|done|terminate`,
		`{"action":"inform"`,
		`{"action":"yield"`,
		`{"action":"done"`,
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("service guide is missing %q", want)
		}
	}
}

func TestServiceActionsValidateOnlyInServiceMode(t *testing.T) {
	for _, raw := range []string{
		`{"action":"inform","payload":"level 3.8 m","reason":"crossed the band"}`,
		`{"action":"yield","payload":{"until":"2026-10-22T12:05:00Z"},"reason":"next sample"}`,
	} {
		resp, err := ParseResponse(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
		if err := resp.ValidateFor(ServiceActions()); err != nil {
			t.Errorf("service mode rejected %s: %v", raw, err)
		}
		if err := resp.ValidateFor(ChatActions()); err == nil {
			t.Errorf("chat mode accepted %s", raw)
		}
	}
}

func TestActionSetOrderAndEnum(t *testing.T) {
	if got := ChatActions().Enum(); got != "tool|ask|done|terminate" {
		t.Errorf("chat enum = %q", got)
	}
	if got := ServiceActions().Enum(); got != "tool|ask|inform|yield|done|terminate" {
		t.Errorf("service enum = %q", got)
	}
	if !ServiceActions().Has(ActionYield) || ChatActions().Has(ActionYield) {
		t.Error("action set membership is wrong")
	}
	if !ActionSet(nil).Has(ActionDone) {
		t.Error("the zero action set should mean chat")
	}
}
