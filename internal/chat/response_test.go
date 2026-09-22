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
	if _, err := ParseResponse(raw); err == nil {
		t.Fatal("expected a parse error for unbalanced JSON")
	}
}
