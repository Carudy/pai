package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/tui"
)

func writeUserTool(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`name = "demo"
brief = "demo tool"
kind = "cli"
description = "echo one value"

[params]
value = { type = "string", required = true }

[cli]
command = %q
args = ["%%s", "{value}"]
`, "echo")
	if err := os.WriteFile(filepath.Join(dir, "demo.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeProjectTool(t *testing.T, workspace string) {
	t.Helper()
	dir := filepath.Join(workspace, ".pai", "tools")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `name = "proj"
brief = "project tool"
kind = "cli"

[cli]
command = "echo"
args = ["hi"]
`
	if err := os.WriteFile(filepath.Join(dir, "proj.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, "pai")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A project tool is visible only from inside a trusted root, and the listing
// says so by layer and source.
func TestToolListShowsTrustedProjectTools(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	workspace := t.TempDir()
	writeProjectTool(t, workspace)
	writeConfig(t, home, fmt.Sprintf("[tool]\ntrusted_projects = [%q]\n", workspace))
	t.Chdir(workspace)

	var out bytes.Buffer
	log := tui.NewLogger(&out, false)
	if code := runTool(context.Background(), []string{"list"}, &out, log); code != 0 {
		t.Fatalf("list: %s", out.String())
	}
	if !strings.Contains(out.String(), "proj") || !strings.Contains(out.String(), "project") {
		t.Fatalf("list output = %q", out.String())
	}

	out.Reset()
	if code := runTool(context.Background(), []string{"show", "proj"}, &out, log); code != 0 {
		t.Fatalf("show: %s", out.String())
	}
	if !strings.Contains(out.String(), "layer:       project") || !strings.Contains(out.String(), "proj.toml") {
		t.Fatalf("show output = %q", out.String())
	}
}

// The same checkouts seen from a config with no trusted_projects contributes
// nothing, so a cloned repository cannot make its definitions callable.
func TestToolListHidesUntrustedProjectTools(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	workspace := t.TempDir()
	writeProjectTool(t, workspace)
	t.Chdir(workspace)

	var out bytes.Buffer
	log := tui.NewLogger(&out, false)
	if code := runTool(context.Background(), []string{"list"}, &out, log); code != 0 {
		t.Fatalf("list: %s", out.String())
	}
	if strings.Contains(out.String(), "proj") {
		t.Fatalf("an untrusted project tool was listed: %q", out.String())
	}

	out.Reset()
	if code := runTool(context.Background(), []string{"show", "proj"}, &out, log); code == 0 {
		t.Fatalf("an untrusted project tool was shown: %q", out.String())
	}
}

// An HTTP definition is checked without executing: the rendered method/URL and
// body are shown, and auth contributes a header name only.
func TestToolCheckHTTPDryRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "pai", "tools")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `name = "search"
brief = "search an API"
kind = "http"

[params]
query = { type = "string", required = true }

[http]
method = "POST"
url = "https://api.example.com/search?q={query}"
auth = "bearer PAI_TEST_TOKEN"
body_params = ["query"]
body_extra = { source = "pai" }
`
	if err := os.WriteFile(filepath.Join(dir, "search.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	log := tui.NewLogger(&out, false)
	if code := runTool(context.Background(), []string{"check", "search", "--json", `{"query":"a/b"}`}, &out, log); code != 0 {
		t.Fatalf("check: %s", out.String())
	}
	got := out.String()
	if !strings.Contains(got, "request: POST https://api.example.com/search?q=a%2Fb") {
		t.Fatalf("request line missing: %q", got)
	}
	if !strings.Contains(got, `body:    {"query":"a/b","source":"pai"}`) {
		t.Fatalf("body missing: %q", got)
	}
	if !strings.Contains(got, "Authorization (from environment)") || strings.Contains(got, "PAI_TEST_TOKEN") {
		t.Fatalf("auth header handling = %q", got)
	}
	if !strings.Contains(got, "executed: no") {
		t.Fatalf("dry-run marker missing: %q", got)
	}
}

func TestToolCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	writeUserTool(t, filepath.Join(home, "pai", "tools"))
	var out bytes.Buffer
	log := tui.NewLogger(&out, false)

	out.Reset()
	if code := runTool(context.Background(), []string{"list"}, &out, log); code != 0 {
		t.Fatalf("list: %s", out.String())
	}
	if !strings.Contains(out.String(), "demo") || !strings.Contains(out.String(), "cli") {
		t.Fatalf("list output = %q", out.String())
	}

	out.Reset()
	if code := runTool(context.Background(), []string{"show", "demo"}, &out, log); code != 0 {
		t.Fatalf("show: %s", out.String())
	}
	if !strings.Contains(out.String(), "kind:        cli") || !strings.Contains(out.String(), "demo.toml") {
		t.Fatalf("show output = %q", out.String())
	}

	out.Reset()
	if code := runTool(context.Background(), []string{"check", "demo", "--json", `{"value":"a; b"}`}, &out, log); code != 0 {
		t.Fatalf("check: %s", out.String())
	}
	if !strings.Contains(out.String(), "argv:") || !strings.Contains(out.String(), `"a; b"`) || !strings.Contains(out.String(), "executed: no") {
		t.Fatalf("check output = %q", out.String())
	}
}
