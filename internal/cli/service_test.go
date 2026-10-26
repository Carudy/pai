package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/session"
	"github.com/Carudy/pai/internal/tui"
)

func writeManifest(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "service.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestServiceCreateListShow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	workspace := t.TempDir()
	path := writeManifest(t, t.TempDir(), `
[service]
goal = "watch the river"
role = "devops"
workspace = "`+workspace+`"
report_every = "30s"

[[watch]]
name = "tick"
kind = "timer"
every = "45s"
`)
	var out bytes.Buffer
	log := tui.NewLogger(&out, false)

	if code := runServiceCmd(context.Background(), []string{"create", "svc", "--file", path}, &out, log); code != 0 {
		t.Fatalf("create: %s", out.String())
	}
	if !strings.Contains(out.String(), "created service svc") || !strings.Contains(out.String(), "next wake:") {
		t.Fatalf("create output = %q", out.String())
	}

	out.Reset()
	if code := runServiceCmd(context.Background(), []string{"list"}, &out, log); code != 0 {
		t.Fatalf("list: %s", out.String())
	}
	if !strings.Contains(out.String(), "svc") || !strings.Contains(out.String(), "watch the river") {
		t.Fatalf("list output = %q", out.String())
	}

	out.Reset()
	if code := runServiceCmd(context.Background(), []string{"show", "svc"}, &out, log); code != 0 {
		t.Fatalf("show: %s", out.String())
	}
	for _, want := range []string{"workspace:", "report_every", "tick", "watchers:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("show output is missing %q:\n%s", want, out.String())
		}
	}

	store, err := session.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	meta, err := store.Get("svc")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Meta.Mode != session.ModeService || meta.Meta.ID == "" || meta.Meta.Cwd != resolved {
		t.Fatalf("stored meta = %+v", meta.Meta)
	}

	// A service is still a session: the generic listing shows its mode, and the
	// generic show works even though the service view is richer.
	var sessionsOut bytes.Buffer
	if code := sessionList(store, &sessionsOut, log); code != 0 {
		t.Fatalf("session list: %s", sessionsOut.String())
	}
	if !strings.Contains(sessionsOut.String(), "MODE") || !strings.Contains(sessionsOut.String(), "service") {
		t.Fatalf("session list does not show the mode: %q", sessionsOut.String())
	}
	var sessionOut bytes.Buffer
	if code := sessionShow(store, "svc", &sessionOut, log); code != 0 {
		t.Fatalf("session show: %s", sessionOut.String())
	}
	if !strings.Contains(sessionOut.String(), "mode:    service") {
		t.Fatalf("session show does not show the mode: %q", sessionOut.String())
	}

	if meta.Meta.NextWake == nil {
		t.Fatal("the initial next wake was not stored")
	}
	ss, ok := store.(session.ServiceStore)
	if !ok {
		t.Fatal("store does not expose service state")
	}
	watchers, err := ss.Watchers(meta.Meta.ID)
	if err != nil || len(watchers) != 1 || watchers[0].Name != "tick" {
		t.Fatalf("watchers = %+v, %v", watchers, err)
	}
	info, err := ss.ServiceInfo("svc")
	if err != nil || !strings.Contains(info.Spec, `"goal":"watch the river"`) {
		t.Fatalf("service info = %+v, %v", info, err)
	}
}

func TestServiceCreateRequiresApprovalForCommandWatchers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	workspace := t.TempDir()
	path := writeManifest(t, t.TempDir(), `
[service]
goal = "probe"
role = "devops"
workspace = "`+workspace+`"

[[watch]]
name = "level"
kind = "command"
every = "30s"
cmd = "true"
trigger = { kind = "changed" }
`)
	var out bytes.Buffer
	log := tui.NewLogger(&out, false)

	if code := runServiceCmd(context.Background(), []string{"create", "probe", "--file", path}, &out, log); code == 0 {
		t.Fatalf("command watcher was accepted without --yes: %s", out.String())
	}
	if !strings.Contains(out.String(), "requires --yes") && !strings.Contains(out.String(), "--yes") {
		t.Fatalf("approval error is unclear: %q", out.String())
	}
	out.Reset()
	if code := runServiceCmd(context.Background(), []string{"create", "probe", "--file", path, "--yes"}, &out, log); code != 0 {
		t.Fatalf("create with --yes: %s", out.String())
	}
}

func TestServiceAlias(t *testing.T) {
	c := findCommand("svc")
	if c == nil || c.name != "service" {
		t.Fatalf("findCommand(\"svc\") = %+v", c)
	}
}

func TestServiceCreateRejectsBadManifestAndDuplicate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	workspace := t.TempDir()
	valid := `
[service]
goal = "g"
role = "devops"
workspace = "` + workspace + `"
`
	path := writeManifest(t, t.TempDir(), valid)
	var out bytes.Buffer
	log := tui.NewLogger(&out, false)
	if code := runServiceCmd(context.Background(), []string{"create", "dup", "--file", path}, &out, log); code != 0 {
		t.Fatalf("create: %s", out.String())
	}
	out.Reset()
	if code := runServiceCmd(context.Background(), []string{"create", "dup", "--file", path}, &out, log); code == 0 {
		t.Fatalf("duplicate service was accepted: %s", out.String())
	}

	bad := writeManifest(t, t.TempDir(), "[service]\ngoal = \"x\"\n")
	out.Reset()
	if code := runServiceCmd(context.Background(), []string{"create", "bad", "--file", bad}, &out, log); code == 0 {
		t.Fatalf("invalid manifest was accepted: %s", out.String())
	}
}
