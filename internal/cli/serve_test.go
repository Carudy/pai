package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/runner"
	"github.com/Carudy/pai/internal/session"
)

func TestServeFlags(t *testing.T) {
	for _, args := range [][]string{{}, {"--ip", "127.0.0.1", "--port", "9384", "--max-active-sessions", "2"}, {"-ip", "127.0.0.1", "-p", "9384"}} {
		f, err := parseServeFlags(args)
		if err != nil || f.ip != "127.0.0.1" || f.port != 9384 || f.maxActive != 2 || f.activityLog {
			t.Fatalf("%v: %+v %v", args, f, err)
		}
	}
	for _, args := range [][]string{{"--ip", "localhost"}, {"-p", "0"}, {"--port", "65536"}, {"--max-active-sessions", "0"}, {"extra"}, {"--unknown"}} {
		if _, err := parseServeFlags(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, arg := range []string{"-h", "--help"} {
		if code := Run(context.Background(), io.Discard, []string{"serve", arg}); code != 0 {
			t.Fatalf("help: %d", code)
		}
	}
}

func TestServeActivityLog(t *testing.T) {
	for _, args := range [][]string{{"--activity-log"}, {"--activity-log=true"}} {
		f, err := parseServeFlags(args)
		if err != nil || !f.activityLog {
			t.Fatalf("activity flag: %+v %v", f, err)
		}
	}
	f, err := parseServeFlags([]string{"--activity-log=false"})
	if err != nil || f.activityLog {
		t.Fatalf("disabled flag: %+v %v", f, err)
	}
	if !strings.Contains(serveHelp(), "--activity-log") {
		t.Fatal("missing help")
	}
	for _, tc := range []struct {
		e    runner.Event
		want string
	}{
		{runner.Event{Type: "tool_call", Data: core.ToolCall{Name: "execute", Target: "sh", Detail: "echo hello\nworld", Reason: "private"}}, "[one]: tool_exec execute sh echo hello world\n"},
		{runner.Event{Type: "tool_call", Data: core.ToolCall{Name: "edit", Target: "file.go", Detail: "private", Diff: "secret"}}, "[one]: tool_exec edit file.go\n"},
		{runner.Event{Type: "prompt", Data: runner.Prompt{Kind: "confirm", Title: "private"}}, "[one]: prompt confirm\n"},
		{runner.Event{Type: "done", Data: "private"}, "[one]: done\n"},
		{runner.Event{Type: "awaiting"}, "[one]: awaiting\n"},
		{runner.Event{Type: "terminate", Data: "private"}, "[one]: terminate\n"},
		{runner.Event{Type: "prompt_cancelled"}, "[one]: prompt_cancelled\n"},
		{runner.Event{Type: "reason", Data: "private"}, ""},
		{runner.Event{Type: "tool_output", Data: "secret"}, ""},
		{runner.Event{Type: "notice", Data: "secret"}, ""},
	} {
		if got := serveActivityLine(runner.Activity{Name: "one", Event: tc.e}); got != tc.want {
			t.Fatalf("%s: %q want %q", tc.e.Type, got, tc.want)
		}
	}
	if got := serveActivityText("a\n\x1bb\u202ec"); got != "a bc" {
		t.Fatalf("controls: %q", got)
	}
	if got := serveActivityText(strings.Repeat("x", 600)); len([]rune(got)) != 513 {
		t.Fatal("unbounded line")
	}
	m := runner.New(context.Background(), nil, 1)
	var out bytes.Buffer
	stop, err := startServeActivityLog(m, &out)
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	stop()
	if out.Len() != 0 {
		t.Fatal("unexpected output")
	}
}

type blockedActivityWriter struct {
	started chan struct{}
	release chan struct{}
}

func (w blockedActivityWriter) Write(p []byte) (int, error) {
	close(w.started)
	<-w.release
	return len(p), nil
}

func TestServeActivityShutdownBound(t *testing.T) {
	m := runner.New(context.Background(), &serveBackend{}, 1)
	defer m.Close()
	w := blockedActivityWriter{make(chan struct{}), make(chan struct{})}
	defer close(w.release)
	stop, err := startServeActivityLog(m, w)
	if err != nil {
		t.Fatal(err)
	}
	// Preparation fails, but its final stopped event must reach the logger.
	if err := m.Send("bad/name", "task"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.started:
	case <-time.After(5 * time.Second):
		t.Fatal("logger did not write")
	}
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked on stdout")
	}
}

func TestServePublicOrigin(t *testing.T) {
	for raw, want := range map[string]string{
		"https://pai.example.com":  "https://pai.example.com",
		"https://PAI.example.com/": "https://pai.example.com",
		"http://localhost:9384/":   "http://localhost:9384",
		"https://[::1]:8443/":      "https://[::1]:8443",
	} {
		f, err := parseServeFlags([]string{"--public-origin", raw})
		if err != nil || f.publicOrigin != want {
			t.Fatalf("%q: %+v %v", raw, f, err)
		}
	}
	for _, raw := range []string{"", "pai.example.com", "//pai.example.com", "ftp://pai.example.com", "https://", "https://user:pass@pai.example.com", "https://pai.example.com/path", "https://pai.example.com//", "https://pai.example.com/%2f", "https://pai.example.com?", "https://pai.example.com?q=x", "https://pai.example.com#", "https://pai.example.com#fragment", "https://pai.example.com:", "https://pai.example.com:0", "https://pai.example.com:65536", "https://pai.example.com:abc", "https://pai.example.com:0443", "https://[example.com]", "https://::1", "https://bad host", " https://pai.example.com", "https://pai.example.com\n"} {
		if _, err := parseServeFlags([]string{"--public-origin", raw}); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	f, err := parseServeFlags([]string{"--public-origin", "https://pai.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", strings.Repeat("x", 31)} {
		t.Setenv("PAI_SERVE_TOKEN", token)
		if _, err := serveToken(f); err == nil {
			t.Fatal("public origin accepted missing/short token on loopback")
		}
	}
	token := strings.Repeat("x", 32)
	t.Setenv("PAI_SERVE_TOKEN", token)
	if got, err := serveToken(f); err != nil || got != token {
		t.Fatalf("token: %v", err)
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAI_SERVE_TOKEN", "")
	f.tokenFile = path
	if got, err := serveToken(f); err != nil || got != token {
		t.Fatalf("file token: %v", err)
	}
	help := serveHelp()
	for _, text := range []string{"--public-origin", "preserving Host", "forwarded headers are never trusted"} {
		if !strings.Contains(help, text) {
			t.Fatalf("help missing %q", text)
		}
	}
}

func TestServeToken(t *testing.T) {
	t.Setenv("PAI_SERVE_TOKEN", "")
	for _, ip := range []string{"127.0.0.1", "::1"} {
		if _, err := serveToken(serveFlags{ip: ip}); err != nil {
			t.Fatal(err)
		}
	}
	for _, ip := range []string{"0.0.0.0", "::", "192.0.2.1", "localhost"} {
		if _, err := serveToken(serveFlags{ip: ip}); err == nil {
			t.Fatalf("accepted %s", ip)
		}
	}
	t.Setenv("PAI_SERVE_TOKEN", strings.Repeat("x", 31))
	if _, err := serveToken(serveFlags{ip: "0.0.0.0"}); err == nil {
		t.Fatal("short token accepted")
	}
	path := filepath.Join(t.TempDir(), "token")
	token := strings.Repeat("y", 32)
	if err := os.WriteFile(path, []byte(" \n"+token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := serveToken(serveFlags{ip: "0.0.0.0", tokenFile: path})
	if err != nil || got != token {
		t.Fatalf("file token: %q %v", got, err)
	}
	t.Setenv("PAI_SERVE_TOKEN", "a\nb")
	if _, err := serveToken(serveFlags{ip: "127.0.0.1"}); err == nil {
		t.Fatal("multiline accepted")
	}
}

func TestServeBackend(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	if err := os.MkdirAll(filepath.Join(cfgDir, "pai"), 0700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "pai", "config.toml")
	writeConfig := func(text string) {
		t.Helper()
		if err := os.WriteFile(cfgPath, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig("[app]\ndefault_model = 'deepseek:test'\ndefault_role = 'devops'\n")
	store, err := session.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	b := &serveBackend{store: store, cwd: cwd}
	cfg, _, rec, _, cleanup, err := b.Prepare("first")
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	for _, content := range []string{"one", "two", "three"} {
		if err := rec.AppendTurn(core.Turn{Role: "user", Kind: "input", Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	h, err := b.History("first", 1, 1)
	if err != nil || h.Total != 3 || len(h.Turns) != 1 || h.Turns[0].Content != "two" {
		t.Fatalf("history: %+v %v", h, err)
	}
	h, err = b.History("first", 100, 1)
	if err != nil || len(h.Turns) != 0 {
		t.Fatalf("past end: %+v %v", h, err)
	}
	for _, page := range [][2]int{{-1, 1}, {0, 0}, {0, 201}, {1000001, 1}} {
		if _, err := b.History("first", page[0], page[1]); err == nil {
			t.Fatal("invalid page accepted")
		}
	}
	cfg.ProvidersConfigs["sentinel"] = cfg.ProvidersConfigs["deepseek"]
	writeConfig("[app]\ndefault_model = 'deepseek:changed'\ndefault_role = 'coder'\n")
	resumed, _, _, history, _, err := b.Prepare("first")
	if err != nil || resumed.DefaultModel != "deepseek:test" || resumed.DefaultRole != "devops" || len(history) != 3 {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	if _, ok := resumed.ProvidersConfigs["sentinel"]; ok {
		t.Fatal("shared config map")
	}
	fresh, _, _, _, _, err := b.Prepare("fresh")
	if err != nil || fresh.DefaultRole != "coder" || fresh.DefaultModel != "deepseek:changed" {
		t.Fatalf("fresh: %+v %v", fresh, err)
	}
	other := t.TempDir()
	if _, err := store.Create(session.Meta{Name: "elsewhere", Role: "devops", Model: "deepseek:test", Cwd: other}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, dir, err := b.PrepareWorkspace("elsewhere", ""); err != nil || dir != other {
		t.Fatalf("saved workspace: %q %v", dir, err)
	}
	if _, _, _, _, _, _, err := b.PrepareWorkspace("elsewhere", cwd); err == nil {
		t.Fatal("workspace mismatch accepted")
	}
	roles, defaultRole, err := b.Roles()
	if err != nil || len(roles) == 0 || defaultRole != "coder" {
		t.Fatalf("roles: %v %q %v", roles, defaultRole, err)
	}
	if _, err := b.CreateWithRole("invalid-role", other, "missing-role"); err == nil {
		t.Fatal("invalid role accepted")
	}
	if _, err := store.Get("invalid-role"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("invalid role persisted: %v", err)
	}
	selected, err := b.CreateWithRole("selected-role", other, "devops")
	if err != nil || selected.Role != "devops" {
		t.Fatalf("selected role: %+v %v", selected, err)
	}
	prepared, _, _, _, _, err := b.Prepare("selected-role")
	if err != nil || prepared.DefaultRole != "devops" {
		t.Fatalf("prepared role: %+v %v", prepared, err)
	}
	meta, err := b.Create("created", other+"/../"+filepath.Base(other))
	if err != nil || meta.Cwd != other {
		t.Fatalf("create: %+v %v", meta, err)
	}
	h, err = b.History("created", 0, 100)
	if err != nil || h.Total != 0 || h.Cwd != other {
		t.Fatalf("empty history: %+v %v", h, err)
	}
	if _, err := b.Create("created", other); err == nil {
		t.Fatal("duplicate creation accepted")
	}
	if _, _, _, _, _, dir, err := b.PrepareWorkspace("created", ""); err != nil || dir != other {
		t.Fatalf("created resume: %q %v", dir, err)
	}
	for _, dir := range []string{filepath.Join(other, "missing"), cfgPath} {
		if _, err := b.Create("invalid-dir", dir); err == nil {
			t.Fatalf("accepted %q", dir)
		}
		if _, _, _, _, _, _, err := b.PrepareWorkspace("invalid-dir", dir); err == nil {
			t.Fatalf("prepared %q", dir)
		}
	}
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, err := b.Prepare("elsewhere"); err == nil {
		t.Fatal("removed workspace accepted")
	}
	writeConfig("[app]\ndefault_model = 'unknown:test'\n")
	if _, err := b.Create("no-provider", cwd); err != nil {
		t.Fatalf("creation required provider: %v", err)
	}
	writeConfig("[app]\ndefault_model = 'deepseek:test'\ndefault_role = 'devops'\n")
	workspace := t.TempDir()
	relative, err := filepath.Rel(cwd, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, dir, err := b.PrepareWorkspace("first-workspace", relative); err != nil || dir != workspace {
		t.Fatalf("first workspace: %q %v", dir, err)
	}
	stored, err := store.Get("first-workspace")
	if err != nil || stored.Meta.Cwd != workspace {
		t.Fatalf("persisted workspace: %+v %v", stored, err)
	}
	for _, text := range []string{"invalid toml !", "[app]\ndefault_role = 'missing-role'\n", "[app]\ndefault_model = 'unknown:test'\n", "[app]\ndefault_model = 'invalid'\n"} {
		writeConfig(text)
		if _, _, _, _, _, err := b.Prepare("failed"); err == nil {
			t.Fatalf("accepted config %q", text)
		}
		if _, err := store.Get("failed"); !errors.Is(err, session.ErrNotFound) {
			t.Fatalf("persisted failure: %v", err)
		}
	}
	if _, _, _, _, _, err := b.Prepare("bad/name"); err == nil {
		t.Fatal("invalid name accepted")
	}
	actual, _ := os.Getwd()
	if actual != cwd {
		t.Fatal("changed cwd")
	}
}
