package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseToolAction(t *testing.T) {
	resp, err := ParseResponse(`{"action":"tool","toolname":"execute","payload":"df -h","reason":"check disk"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := resp.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if resp.Action != ActionTool {
		t.Errorf("action = %q", resp.Action)
	}
	if resp.ToolName != "execute" {
		t.Errorf("toolname = %q", resp.ToolName)
	}
	if resp.Reason != "check disk" {
		t.Errorf("reason = %q", resp.Reason)
	}

	var args string
	if err := json.Unmarshal(resp.Payload, &args); err != nil || args != "df -h" {
		t.Errorf("payload = %s (err %v)", resp.Payload, err)
	}
}

// A tool whose arguments are an object (remote) is unchanged: the object is the
// payload, and the tool name is now beside it rather than around it.
func TestParseToolActionObjectArguments(t *testing.T) {
	resp, err := ParseResponse(`{"action":"tool","toolname":"remote","payload":{"host":"nb","cmd":"ls"},"reason":"r"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := resp.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if resp.ToolName != "remote" {
		t.Errorf("toolname = %q", resp.ToolName)
	}
	if !strings.Contains(string(resp.Payload), `"host"`) || !strings.Contains(string(resp.Payload), `"cmd"`) {
		t.Errorf("payload = %s, want the tool's arguments", resp.Payload)
	}
}

// The old nested shape is now invalid — it must not silently pass as a tool
// action with no tool name.
func TestValidateRejectsNestedToolShape(t *testing.T) {
	resp, err := ParseResponse(`{"action":"tool","payload":{"toolname":"execute","payload":"df -h"},"reason":"r"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := resp.Validate(); err == nil {
		t.Error("expected the nested tool shape to be rejected")
	}
}

func TestValidate(t *testing.T) {
	valid := []string{
		`{"action":"ask","payload":"which host?","reason":"r"}`,
		`{"action":"done","payload":"finished","reason":"r"}`,
		`{"action":"terminate","payload":"cannot proceed","reason":"r"}`,
		`{"action":"tool","toolname":"execute","payload":"ls","reason":"r"}`,
		ToolExample(),
	}
	for _, raw := range valid {
		resp, err := ParseResponse(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
		if err := resp.Validate(); err != nil {
			t.Errorf("validate %s: %v", raw, err)
		}
	}

	invalid := map[string]string{
		"unknown action":    `{"action":"frobnicate","payload":"x","reason":"r"}`,
		"missing action":    `{"payload":"x","reason":"r"}`,
		"empty payload":     `{"action":"done","payload":"","reason":"r"}`,
		"tool without name": `{"action":"tool","payload":"df -h","reason":"r"}`,
	}
	for name, raw := range invalid {
		resp, err := ParseResponse(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
		if err := resp.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

// The example shown to the model has to be a valid response itself.
func TestGuideExampleIsValid(t *testing.T) {
	resp, err := ParseResponse(ToolExample())
	if err != nil {
		t.Fatalf("parse example: %v", err)
	}
	if err := resp.Validate(); err != nil {
		t.Fatalf("the guide's own example does not validate: %v", err)
	}
	if !strings.Contains(OutputGuide(), ToolExample()) {
		t.Error("the guide should show the tool example verbatim")
	}
}

// A response with a missing closing brace (a model miscounting nesting) must be
// reported as unparseable rather than half-applied.
func TestParseUnbalancedIsError(t *testing.T) {
	raw := `{"action":"tool","toolname":"remote","payload":{"host":"h","cmd":"ls"},"reason":"r"`
	_, err := ParseResponse(raw)
	if err == nil {
		t.Fatal("expected a parse error for unbalanced JSON")
	}
	// The message should point at truncation rather than a cryptic decoder error.
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("error = %v, want it to mention truncation", err)
	}
}

// Prose and stray braces around the object are tolerated.
func TestParseSurroundedByProse(t *testing.T) {
	raw := "Sure, here you go:\n\n```json\n{\"action\":\"done\",\"payload\":\"all set\",\"reason\":\"r\"}\n```\n\nLet me know if you need {anything} else."
	resp, err := ParseResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Action != ActionDone || resp.GetPayload() != "all set" {
		t.Errorf("got action=%q payload=%q", resp.Action, resp.GetPayload())
	}
}

// A brace inside a string value must not shift the object boundary.
func TestParseBraceInsideString(t *testing.T) {
	raw := `{"action":"done","payload":"use {} for maps, and {a} for sets","reason":"r"}`
	resp, err := ParseResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.GetPayload() != "use {} for maps, and {a} for sets" {
		t.Errorf("payload = %q", resp.GetPayload())
	}
}

// Two objects back-to-back: the decoder must not concatenate them (the old
// first-{-to-last-} span did, producing invalid JSON).
func TestParsePicksFirstOfTwoObjects(t *testing.T) {
	raw := `{"action":"done","payload":"first","reason":"r"} {"action":"done","payload":"second","reason":"r"}`
	resp, err := ParseResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.GetPayload() != "first" {
		t.Errorf("payload = %q, want the first object", resp.GetPayload())
	}
}

// A non-response JSON object in the prose must not shadow the real response.
func TestParseSkipsUnrecognizedObject(t *testing.T) {
	raw := `The config looks like {"temperature":0.5} but here is my answer: {"action":"done","payload":"ok","reason":"r"}`
	resp, err := ParseResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Action != ActionDone || resp.GetPayload() != "ok" {
		t.Errorf("got action=%q payload=%q; the unrecognized object won", resp.Action, resp.GetPayload())
	}
}

// A lone object with no recognized action still parses, so Validate (not Parse)
// reports the field-level problem and the retry loop can correct it.
func TestParseNoActionDefersToValidate(t *testing.T) {
	resp, err := ParseResponse(`{"payload":"x","reason":"r"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := resp.Validate(); err == nil {
		t.Error("expected Validate to reject a missing action")
	}
}

func TestJSONCandidates(t *testing.T) {
	got := jsonCandidates(`a {x} b {"n":1} c`)
	want := []string{`{x}`, `{"n":1}`}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %q, want %q", i, got[i], want[i])
		}
	}
}
