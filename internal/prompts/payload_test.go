package prompts

import (
	"encoding/json"
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }

func testTool() ResolvedTool {
	return ResolvedTool{
		Name: "demo",
		Kind: ToolCLI,
		Params: []Param{
			{Name: "dataset", Type: ParamString, Required: true, MaxLength: 20, Pattern: "^[a-z]+$"},
			{Name: "folds", Type: ParamInteger, Default: int64(5), Min: f64(2), Max: f64(10)},
			{Name: "dry", Type: ParamBoolean, Default: false},
			{Name: "mode", Type: ParamEnum, Default: "fast", Values: []string{"fast", "slow"}},
			{Name: "ratio", Type: ParamNumber, Default: 0.5, Min: f64(0), Max: f64(1)},
		},
		CLI: &CLISpec{
			Command: "demo",
			Args:    []string{"--dataset", "{dataset}", "--folds", "{folds}", "--dry={dry}", "--mode", "{mode}", "--ratio={ratio}"},
		},
	}
}

func TestValidatePayloadAppliesDefaults(t *testing.T) {
	tool := testTool()
	values, err := tool.ValidatePayload(json.RawMessage(`{"dataset":"sonar","folds":7,"dry":true,"mode":"slow"}`))
	if err != nil {
		t.Fatal(err)
	}
	if values["dataset"] != "sonar" || values["folds"] != int64(7) || values["dry"] != true || values["mode"] != "slow" {
		t.Fatalf("values = %#v", values)
	}
	if values["ratio"] != 0.5 {
		t.Fatalf("ratio default = %#v", values["ratio"])
	}
}

func TestValidatePayloadRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"not an object", `["x"]`, "JSON object"},
		{"missing required", `{"folds":4}`, "missing required parameter"},
		{"unknown field", `{"dataset":"sonar","nope":1}`, "unknown parameter"},
		{"wrong type", `{"dataset":4}`, "must be a string"},
		{"below min", `{"dataset":"sonar","folds":1}`, "at least 2"},
		{"above max", `{"dataset":"sonar","folds":11}`, "at most 10"},
		{"pattern", `{"dataset":"SONAR"}`, "does not match"},
		{"long", `{"dataset":"abcdefghijklmnopqrstuvwxyz"}`, "longer than 20"},
		{"enum", `{"dataset":"sonar","mode":"medium"}`, "must be one of"},
		{"number range", `{"dataset":"sonar","ratio":2}`, "at most 1"},
		{"null", `{"dataset":null}`, "must not be null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := testTool().ValidatePayload(json.RawMessage(tc.payload))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRenderArgsKeepsValuesVerbatim(t *testing.T) {
	tool := ResolvedTool{
		Name: "demo",
		Kind: ToolCLI,
		Params: []Param{
			{Name: "value", Type: ParamString, Required: true},
		},
		CLI: &CLISpec{Command: "demo", Args: []string{"--value", "{value}"}},
	}
	values, err := tool.ValidatePayload(json.RawMessage(`{"value":"a; b $(echo injected)"}`))
	if err != nil {
		t.Fatal(err)
	}
	args, err := tool.RenderArgs(values)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[1] != "a; b $(echo injected)" {
		t.Fatalf("args = %#v", args)
	}
}

func TestRenderArgsFormatsTypedValues(t *testing.T) {
	values := map[string]any{
		"dataset": "sonar",
		"folds":   int64(7),
		"dry":     true,
		"mode":    "slow",
		"ratio":   0.25,
	}
	args, err := testTool().RenderArgs(values)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--dataset", "sonar", "--folds", "7", "--dry=true", "--mode", "slow", "--ratio=0.25"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func httpTool() ResolvedTool {
	return ResolvedTool{
		Name: "search",
		Kind: ToolHTTP,
		Params: []Param{
			{Name: "query", Type: ParamString, Required: true},
			{Name: "limit", Type: ParamInteger, Default: int64(5)},
			{Name: "flag", Type: ParamBoolean, Default: true},
		},
		HTTP: &HTTPSpec{
			Method:     "GET",
			URL:        "https://api.example.test/search/{query}/items?q={query}&n={limit}&flag={flag}",
			BodyParams: []string{"query", "limit"},
			BodyExtra:  map[string]string{"source": "pai"},
		},
	}
}

// A value in the path cannot add a segment, and a value in the query cannot
// add a query pair: each position uses its own escaping.
func TestRenderURLEscapesByPosition(t *testing.T) {
	tool := httpTool()
	values, err := tool.ValidatePayload(json.RawMessage(`{"query":"a/b&c=d ?#"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := tool.RenderURL(values)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://api.example.test/search/a%2Fb&c=d%20%3F%23/items?q=a%2Fb%26c%3Dd+%3F%23&n=5&flag=true"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestRenderURLRequiresValues(t *testing.T) {
	tool := httpTool()
	if _, err := tool.RenderURL(map[string]any{}); err == nil || !strings.Contains(err.Error(), "has no value") {
		t.Fatalf("err = %v, want a missing-value error", err)
	}
}

// The body is an encoded object, so a string value cannot change its shape.
func TestRenderBodyEncodesParamsAndExtras(t *testing.T) {
	tool := httpTool()
	tool.HTTP.Method = "POST"
	values, err := tool.ValidatePayload(json.RawMessage(`{"query":"{\"evil\":true}","limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := tool.RenderBody(values)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"limit":3,"query":"{\"evil\":true}","source":"pai"}`
	if string(body) != want {
		t.Fatalf("body = %s, want %s", body, want)
	}
}

func TestRenderBodyOmitsGetBody(t *testing.T) {
	body, err := httpTool().RenderBody(map[string]any{"query": "x", "limit": int64(1)})
	if err != nil || body != nil {
		t.Fatalf("RenderBody = (%q, %v), want (nil, nil)", body, err)
	}
}
