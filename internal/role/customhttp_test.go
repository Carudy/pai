package role

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/prompts"
)

// An HTTP tool is a definition-owned call: the parameter fills the query, the
// auth header comes from the environment, and the response is a normal
// observation. A project-defined HTTP tool prompts even with trust-all on and
// ignores a config grant.
func TestProjectHTTPToolEndToEnd(t *testing.T) {
	var gotQuery, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":["x","y"]}`))
	}))
	defer server.Close()
	t.Setenv("PAI_TEST_HTTP_TOKEN", "s3cret")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	workspace := t.TempDir()
	toolsDir := filepath.Join(workspace, ".pai", "tools")
	if err := os.MkdirAll(toolsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	def := fmt.Sprintf(`name = "search"
brief = "search an API"
kind = "http"
description = "search"

[params]
query = { type = "string", required = true }

[http]
method = "GET"
url = %q
auth = "bearer PAI_TEST_HTTP_TOKEN"
max_bytes = 4096
`, server.URL+"/search?q={query}")
	if err := os.WriteFile(filepath.Join(toolsDir, "search.toml"), []byte(def), 0o600); err != nil {
		t.Fatal(err)
	}

	rp, err := chat.LoadRolePromptForSession("coder", "", nil, []string{workspace}, workspace, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !rp.Resolved.Has("search") || !strings.Contains(rp.Head(), "## Tool: search") {
		t.Fatal("the project HTTP tool is missing from the prompt")
	}

	fp := &sequenceProvider{replies: []string{
		`{"action":"tool","toolname":"search","payload":{"query":"a/b"},"reason":"look it up"}`,
		`{"action":"done","payload":"ok","reason":"finished"}`,
	}}
	prompter := &toolPrompter{}
	obs := &fakeObserver{}
	cfg := &config.UserConfig{
		DefaultRole:     "coder",
		DefaultModel:    "test:model",
		Context:         config.ContextConfig{KeepTurns: 4, ExecLimit: 8000, HeadLines: 80, TailLines: 40},
		TrustedProjects: []string{workspace},
		// A config grant for the same name must not cover a project definition.
		TrustedTools: []string{"search"},
	}
	rt := &Runtime{Observer: obs, Prompter: prompter, Logger: nopLogger{}, Provider: fp}
	if err := runChat(context.Background(), cfg, rt, rp, nil, "search"); err != nil {
		t.Fatal(err)
	}

	if gotQuery != "a/b" || gotAuth != "Bearer s3cret" {
		t.Fatalf("request = (query %q, auth %q)", gotQuery, gotAuth)
	}
	if prompter.confirmed != 1 || len(prompter.untrusted) != 1 || prompter.untrusted[0] != "search" {
		t.Fatalf("confirmation = %+v", prompter)
	}
	if prompter.persistent || prompter.trustAll {
		t.Fatalf("project HTTP grant = (persistent=%v, trustAll=%v), want session-only", prompter.persistent, prompter.trustAll)
	}
	if len(obs.toolCalls) != 1 {
		t.Fatalf("tool calls = %+v", obs.toolCalls)
	}
	call := obs.toolCalls[0]
	if call.Name != "search" || call.Trusted || !strings.HasPrefix(call.Detail, "GET "+server.URL) {
		t.Fatalf("tool call = %+v", call)
	}
	if call.Source == "" || !strings.Contains(call.Source, filepath.Join(".pai", "tools", "search.toml")) {
		t.Fatalf("source = %q", call.Source)
	}

	var result string
	for _, m := range fp.seen[1] {
		if strings.Contains(m.Content, "[search result]: HTTP 200") {
			result = m.Content
		}
	}
	if result == "" || !strings.Contains(result, `"items"`) {
		t.Fatalf("second request is missing the HTTP result:\n%s", transcript(fp.seen[1]))
	}
}

func TestBodyPreview(t *testing.T) {
	if got := bodyPreview(nil); got != "" {
		t.Fatalf("empty preview = %q", got)
	}
	if got := bodyPreview([]byte(`{"q":"x"}`)); got != `{"q":"x"}` {
		t.Fatalf("short preview = %q", got)
	}
	long := []byte(strings.Repeat("é", 400))
	got := bodyPreview(long)
	if !strings.HasSuffix(got, "…") || len(got) > maxHTTPBodyPreview+len("…") {
		t.Fatalf("long preview = %q (%d bytes)", got, len(got))
	}
	if !utf8.ValidString(strings.TrimSuffix(got, "…")) {
		t.Fatal("preview split a UTF-8 rune")
	}
}

func TestResolveHTTPHeaders(t *testing.T) {
	t.Setenv("PAI_TEST_BEARER", "tok")
	t.Setenv("PAI_TEST_HEADER", "hdr")
	t.Setenv("PAI_TEST_NEWLINE", "a\nb")

	basic, err := resolveHTTPHeaders(&prompts.HTTPSpec{Headers: map[string]string{"Accept": "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	if basic.Get("Accept") != "application/json" {
		t.Fatalf("fixed header = %v", basic)
	}

	bearer, err := resolveHTTPHeaders(&prompts.HTTPSpec{Auth: "bearer PAI_TEST_BEARER"})
	if err != nil {
		t.Fatal(err)
	}
	if bearer.Get("Authorization") != "Bearer tok" {
		t.Fatalf("bearer = %v", bearer)
	}

	header, err := resolveHTTPHeaders(&prompts.HTTPSpec{Auth: "header X-Api-Key PAI_TEST_HEADER"})
	if err != nil {
		t.Fatal(err)
	}
	if header.Get("X-Api-Key") != "hdr" {
		t.Fatalf("header = %v", header)
	}

	if _, err := resolveHTTPHeaders(&prompts.HTTPSpec{Auth: "bearer PAI_TEST_MISSING"}); err == nil || !strings.Contains(err.Error(), "not set") {
		t.Fatalf("missing env error = %v", err)
	}
	if _, err := resolveHTTPHeaders(&prompts.HTTPSpec{Auth: "bearer PAI_TEST_NEWLINE"}); err == nil || !strings.Contains(err.Error(), "newline") {
		t.Fatalf("newline env error = %v", err)
	}
}
