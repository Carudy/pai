package role

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

// sequenceProvider returns one scripted reply per call and records the messages.
type sequenceProvider struct {
	replies []string
	calls   int
	seen    [][]provider.Message
}

func (p *sequenceProvider) Completion(_ context.Context, params provider.CompletionParams) (*provider.ChatCompletion, error) {
	if p.calls >= len(p.replies) {
		return nil, fmt.Errorf("unexpected request %d", p.calls+1)
	}
	p.seen = append(p.seen, append([]provider.Message(nil), params.Messages...))
	reply := p.replies[p.calls]
	p.calls++
	return &provider.ChatCompletion{Choices: []provider.Choice{{
		Message:      provider.Message{Role: provider.RoleAssistant, Content: reply},
		FinishReason: "stop",
	}}}, nil
}

func (p *sequenceProvider) CompletionStream(context.Context, provider.CompletionParams) (<-chan provider.ChatCompletionChunk, <-chan error) {
	panic("streaming is disabled in this test")
}

type toolPrompter struct {
	confirmed  int
	title      string
	untrusted  []string
	persistent bool
	trustAll   bool
}

func (p *toolPrompter) Ask(string) (string, error)   { return "", core.ErrAborted }
func (p *toolPrompter) Confirm(string) (bool, error) { p.confirmed++; return true, nil }
func (p *toolPrompter) ConfirmTool(title string, untrusted []string, persistent, trustAll bool) (core.TrustChoice, error) {
	p.confirmed++
	p.title = title
	p.untrusted = append([]string(nil), untrusted...)
	p.persistent = persistent
	p.trustAll = trustAll
	return core.TrustOnce, nil
}

func TestResolveToolEnv(t *testing.T) {
	t.Setenv("PAI_TEST_SECRET", "value")
	env, err := resolveToolEnv(map[string]string{
		"A": "env:PAI_TEST_SECRET",
		"B": "literal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["A"] != "value" || env["B"] != "literal" {
		t.Fatalf("env = %#v", env)
	}
	if _, err := resolveToolEnv(map[string]string{"A": "env:PAI_TEST_MISSING"}); err == nil {
		t.Fatal("a missing env reference was accepted")
	}
}

func TestProjectCLIToolEndToEnd(t *testing.T) {
	printf, err := exec.LookPath("printf")
	if err != nil {
		t.Skipf("printf is not available: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no user tools; only the project layer
	workspace := t.TempDir()
	toolsDir := filepath.Join(workspace, ".pai", "tools")
	if err := os.MkdirAll(toolsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	toolDef := fmt.Sprintf(`name = "proj"
brief = "project tool"
kind = "cli"
description = "echo one value"

[params]
value = { type = "string", required = true }

[cli]
command = %q
args = ["%%s\n", "{value}"]
`, printf)
	if err := os.WriteFile(filepath.Join(toolsDir, "proj.toml"), []byte(toolDef), 0o600); err != nil {
		t.Fatal(err)
	}

	rp, err := chat.LoadRolePromptForSession("coder", "", nil, []string{workspace}, workspace, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !rp.Resolved.Has("proj") || !slices.Contains(rp.ToolNames(), "proj") {
		t.Fatalf("project tool is missing from the resolved set: %v", rp.ToolNames())
	}
	if !strings.Contains(rp.Head(), "## Tool: proj") {
		t.Fatal("project tool is missing from the prompt head")
	}
	if err := checkToolCoverage(rp); err != nil {
		t.Fatal(err)
	}

	fp := &sequenceProvider{replies: []string{
		`{"action":"tool","toolname":"proj","payload":{"value":"project run"},"reason":"run it"}`,
		`{"action":"done","payload":"ok","reason":"finished"}`,
	}}
	prompter := &toolPrompter{}
	obs := &fakeObserver{}
	cfg := &config.UserConfig{
		DefaultRole:     "coder",
		DefaultModel:    "test:model",
		Streaming:       false,
		Context:         config.ContextConfig{KeepTurns: 4, ExecLimit: 8000, HeadLines: 80, TailLines: 40},
		TrustedProjects: []string{workspace},
		// A config grant for the same name must not cover a project definition.
		TrustedTools: []string{"proj"},
	}
	rt := &Runtime{Observer: obs, Prompter: prompter, Logger: nopLogger{}, Provider: fp}
	if err := loop(context.Background(), cfg, rt, rp, nil, "run proj"); err != nil {
		t.Fatal(err)
	}
	if prompter.confirmed != 1 || len(prompter.untrusted) != 1 || prompter.untrusted[0] != "proj" {
		t.Fatalf("confirmation = %+v", prompter)
	}
	if prompter.persistent || prompter.trustAll {
		t.Fatalf("project tool grant = (persistent=%v, trustAll=%v), want session-only", prompter.persistent, prompter.trustAll)
	}
	if len(obs.toolCalls) != 1 || obs.toolCalls[0].Name != "proj" || obs.toolCalls[0].Trusted {
		t.Fatalf("tool calls = %+v", obs.toolCalls)
	}
	if !strings.Contains(obs.toolCalls[0].Source, filepath.Join(".pai", "tools", "proj.toml")) {
		t.Fatalf("source = %q, want the project definition", obs.toolCalls[0].Source)
	}
	var result string
	for _, m := range fp.seen[1] {
		if strings.Contains(m.Content, "[proj result]: exit code 0") {
			result = m.Content
		}
	}
	if result == "" || !strings.Contains(result, "project run") {
		t.Fatalf("second request is missing the project tool result:\n%s", transcript(fp.seen[1]))
	}
}

func TestCustomCLIToolEndToEnd(t *testing.T) {
	printf, err := exec.LookPath("printf")
	if err != nil {
		t.Skipf("printf is not available: %v", err)
	}
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	rolesDir := filepath.Join(cfgHome, "pai", "roles")
	toolsDir := filepath.Join(cfgHome, "pai", "tools")
	if err := os.MkdirAll(rolesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(toolsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	roleDef := `name = "custom"
description = "custom test role"
tools = ["demo"]
intro = "test role"
`
	if err := os.WriteFile(filepath.Join(rolesDir, "custom.toml"), []byte(roleDef), 0o600); err != nil {
		t.Fatal(err)
	}
	toolDef := fmt.Sprintf(`name = "demo"
brief = "demo tool"
kind = "cli"
description = "echo one value"

[params]
value = { type = "string", required = true }

[cli]
command = %q
args = ["%%s\n", "{value}"]
`, printf)
	if err := os.WriteFile(filepath.Join(toolsDir, "demo.toml"), []byte(toolDef), 0o600); err != nil {
		t.Fatal(err)
	}

	rp, err := chat.LoadRolePromptAt("custom", "", "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkToolCoverage(rp); err != nil {
		t.Fatal(err)
	}
	fp := &sequenceProvider{replies: []string{
		`{"action":"tool","toolname":"demo","payload":{"value":"a; b $(echo injected)"},"reason":"run it"}`,
		`{"action":"done","payload":"ok","reason":"finished"}`,
	}}
	prompter := &toolPrompter{}
	obs := &fakeObserver{}
	cfg := &config.UserConfig{
		DefaultRole:  "custom",
		DefaultModel: "test:model",
		Streaming:    false,
		Context:      config.ContextConfig{KeepTurns: 4, ExecLimit: 8000, HeadLines: 80, TailLines: 40},
	}
	rt := &Runtime{Observer: obs, Prompter: prompter, Logger: nopLogger{}, Provider: fp}

	if err := loop(context.Background(), cfg, rt, rp, nil, "run demo"); err != nil {
		t.Fatal(err)
	}
	if fp.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", fp.calls)
	}
	if prompter.confirmed != 1 || len(prompter.untrusted) != 1 || prompter.untrusted[0] != "demo" {
		t.Fatalf("confirmation = %+v", prompter)
	}
	if !prompter.persistent || !prompter.trustAll {
		t.Fatalf("user tool grant = (persistent=%v, trustAll=%v), want a persistent grant", prompter.persistent, prompter.trustAll)
	}
	if len(obs.toolCalls) != 1 {
		t.Fatalf("tool calls = %+v", obs.toolCalls)
	}
	call := obs.toolCalls[0]
	if call.Name != "demo" || call.Trusted || !strings.Contains(call.Source, "demo.toml") {
		t.Fatalf("tool call = %+v", call)
	}
	var result string
	for _, m := range fp.seen[1] {
		if strings.Contains(m.Content, "[demo result]: exit code 0") {
			result = m.Content
		}
	}
	if result == "" {
		t.Fatalf("second request is missing the tool result:\n%s", transcript(fp.seen[1]))
	}
	if !strings.Contains(result, "a; b $(echo injected)") {
		t.Fatalf("result = %q; the argument was not passed verbatim", result)
	}

	// role-extra.toml can add the same tool to a built-in role without copying it.
	extra, err := chat.LoadRolePromptWithTools("coder", "", []string{"demo"}, "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !extra.Resolved.Has("demo") {
		t.Fatal("role-extra tool is missing from the resolved set")
	}
	if !slices.Contains(extra.ToolNames(), "demo") {
		t.Fatalf("role-extra tool is missing from the prompt: %v", extra.ToolNames())
	}
	if !strings.Contains(extra.Head(), "## Tool: demo") {
		t.Fatal("role-extra tool is missing from the prompt head")
	}

	// A config-level trusted_tools grant skips the confirmation.
	prompter2 := &toolPrompter{}
	obs2 := &fakeObserver{}
	fp2 := &sequenceProvider{replies: []string{
		`{"action":"tool","toolname":"demo","payload":{"value":"trusted"},"reason":"run it"}`,
		`{"action":"done","payload":"ok","reason":"finished"}`,
	}}
	trustedCfg := *cfg
	trustedCfg.TrustedTools = []string{"demo"}
	rt2 := &Runtime{Observer: obs2, Prompter: prompter2, Logger: nopLogger{}, Provider: fp2}
	if err := loop(context.Background(), &trustedCfg, rt2, rp, nil, "run demo"); err != nil {
		t.Fatal(err)
	}
	if prompter2.confirmed != 0 {
		t.Fatalf("trusted tool prompted %d times", prompter2.confirmed)
	}
	if len(obs2.toolCalls) != 1 || !obs2.toolCalls[0].Trusted {
		t.Fatalf("trusted tool call = %+v", obs2.toolCalls)
	}
}
