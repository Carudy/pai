package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/runner"
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
report_interval = "30s"

[[watcher]]
name = "tick"
interval = "45s"
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
	for _, want := range []string{"workspace:", "report_interval", "tick", "watchers:"} {
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
	if err != nil || len(watchers) != 2 {
		t.Fatalf("watchers = %+v, %v", watchers, err)
	}
	// report_interval is stored as the reserved timer watcher, next to the
	// manifest's own probes.
	names := map[string]bool{}
	for _, w := range watchers {
		names[w.Name] = true
	}
	if !names["tick"] || !names[config.ReportWatcherName] {
		t.Fatalf("watcher names = %v, want tick and %s", names, config.ReportWatcherName)
	}
	info, err := ss.ServiceInfo("svc")
	if err != nil || !strings.Contains(info.Spec, `"goal":"watch the river"`) {
		t.Fatalf("service info = %+v, %v", info, err)
	}
}

// A command watcher no longer blocks the write and no longer needs a panel
// grant: the manifest is stored, the first probe asks for confirmation, and
// `--yes` (or the form's box) pre-approves it instead.
func TestServiceCreateDefersCommandApproval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	workspace := t.TempDir()
	path := writeManifest(t, t.TempDir(), `
[service]
goal = "probe"
role = "devops"
workspace = "`+workspace+`"

[[watcher]]
name = "level"
interval = "30s"
command = "true"
`)
	var out bytes.Buffer
	log := tui.NewLogger(&out, false)

	if code := runServiceCmd(context.Background(), []string{"create", "probe", "--file", path}, &out, log); code != 0 {
		t.Fatalf("create without --yes: %s", out.String())
	}
	if !strings.Contains(out.String(), "first run asks for confirmation") {
		t.Fatalf("the confirmation hint is missing: %q", out.String())
	}
	checkApproval := func(name string, want bool) {
		t.Helper()
		store, err := session.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		ss, ok := store.(session.ServiceStore)
		if !ok {
			t.Fatal("store does not expose service state")
		}
		info, err := ss.ServiceInfo(name)
		if err != nil {
			t.Fatal(err)
		}
		watchers, err := ss.Watchers(info.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range watchers {
			if w.Name != "level" {
				continue
			}
			if got := w.ApprovedAt != nil; got != want {
				t.Fatalf("%s: approved = %v, want %v", name, got, want)
			}
			return
		}
		t.Fatalf("%s: the level watcher was not stored", name)
	}
	checkApproval("probe", false)

	// A never-started service is idle enough to edit, so --yes is the documented
	// way to pre-approve the probes it already stored.
	out.Reset()
	if code := runServiceCmd(context.Background(), []string{"edit", "probe", "--file", path, "--yes"}, &out, log); code != 0 {
		t.Fatalf("edit with --yes: %s", out.String())
	}
	checkApproval("probe", true)

	out.Reset()
	if code := runServiceCmd(context.Background(), []string{"create", "granted", "--file", path, "--yes"}, &out, log); code != 0 {
		t.Fatalf("create with --yes: %s", out.String())
	}
	if strings.Contains(out.String(), "first run asks for confirmation") {
		t.Fatalf("--yes still left a pending confirmation: %q", out.String())
	}
	checkApproval("granted", true)
}

// Deleting a service is a whole-row operation: the transcript, queued events, and
// watcher grants go with it. A chat session is refused so a typo cannot hit it.
func TestServiceDelete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	workspace := t.TempDir()
	path := writeManifest(t, t.TempDir(), `
[service]
goal = "probe"
role = "devops"
workspace = "`+workspace+`"

[[watcher]]
name = "level"
interval = "30s"
command = "true"
`)
	var out bytes.Buffer
	log := tui.NewLogger(&out, false)
	if code := runServiceCmd(context.Background(), []string{"create", "probe", "--file", path, "--yes"}, &out, log); code != 0 {
		t.Fatalf("create: %s", out.String())
	}

	store, err := session.Open()
	if err != nil {
		t.Fatal(err)
	}
	ss, ok := store.(session.ServiceStore)
	if !ok {
		t.Fatal("store does not expose service state")
	}
	if _, err := store.Create(session.Meta{Name: "chatty", Role: "devops"}); err != nil {
		t.Fatal(err)
	}
	info, err := ss.ServiceInfo("probe")
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	// A chat session is not a service: refuse, and leave it in place.
	out.Reset()
	if code := runServiceCmd(context.Background(), []string{"rm", "chatty"}, &out, log); code == 0 {
		t.Fatalf("a chat session was deleted by service rm: %s", out.String())
	}
	if !strings.Contains(out.String(), "not a service") {
		t.Fatalf("refusal is unclear: %q", out.String())
	}

	out.Reset()
	if code := runServiceCmd(context.Background(), []string{"rm", "probe"}, &out, log); code != 0 {
		t.Fatalf("rm: %s", out.String())
	}

	store, err = session.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Get("probe"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("deleted service still resolves: %v", err)
	}
	if _, err := store.Get("chatty"); err != nil {
		t.Fatalf("unrelated chat session removed: %v", err)
	}
	ss, _ = store.(session.ServiceStore)
	watchers, err := ss.Watchers(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(watchers) != 0 {
		t.Fatalf("watchers survived the delete: %+v", watchers)
	}
}

func TestServiceStopStart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	workspace := t.TempDir()
	path := writeManifest(t, t.TempDir(), `
[service]
goal = "tick"
role = "devops"
workspace = "`+workspace+`"

[[watcher]]
name = "tick"
interval = "30s"
`)
	var out bytes.Buffer
	log := tui.NewLogger(&out, false)
	if code := runServiceCmd(context.Background(), []string{"create", "svc", "--file", path}, &out, log); code != 0 {
		t.Fatalf("create: %s", out.String())
	}

	out.Reset()
	if code := runServiceCmd(context.Background(), []string{"stop", "svc"}, &out, log); code != 0 {
		t.Fatalf("stop: %s", out.String())
	}
	store, err := session.Open()
	if err != nil {
		t.Fatal(err)
	}
	ss := store.(session.ServiceStore)
	info, err := ss.ServiceInfo("svc")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if info.State != runner.StateStopped || info.NextWake != nil {
		store.Close()
		t.Fatalf("after stop: state = %q next = %v", info.State, info.NextWake)
	}
	store.Close()

	out.Reset()
	if code := runServiceCmd(context.Background(), []string{"start", "svc"}, &out, log); code != 0 {
		t.Fatalf("start: %s", out.String())
	}
	store, err = session.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info, err = store.(session.ServiceStore).ServiceInfo("svc")
	if err != nil {
		t.Fatal(err)
	}
	if info.State != runner.StateSuspended || info.NextWake == nil {
		t.Fatalf("after start: state = %q next = %v", info.State, info.NextWake)
	}
}

// Editing is a manifest replacement for a stopped service: history and identity
// stay, while the role, workspace, spec, and watcher set are reconciled.
func TestServiceEditWhileStopped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	workspace := t.TempDir()
	path := writeManifest(t, t.TempDir(), `
[service]
goal = "first"
role = "devops"
workspace = "`+workspace+`"
report_interval = "30s"

[[watcher]]
name = "tick"
interval = "30s"
`)
	var out bytes.Buffer
	log := tui.NewLogger(&out, false)
	if code := runServiceCmd(context.Background(), []string{"create", "svc", "--file", path}, &out, log); code != 0 {
		t.Fatalf("create: %s", out.String())
	}

	// A started service refuses the edit; a never-run one (no state yet) does not,
	// because there is no activation to race.
	stateStore, err := session.Open()
	if err != nil {
		t.Fatal(err)
	}
	stateSS, ok := stateStore.(session.ServiceStore)
	if !ok {
		t.Fatal("store does not expose service state")
	}
	if err := stateSS.SetServiceState("svc", "suspended", ""); err != nil {
		t.Fatal(err)
	}
	stateStore.Close()

	newWorkspace := t.TempDir()
	edited := writeManifest(t, t.TempDir(), `
[service]
goal = "edited"
role = "coder"
workspace = "`+newWorkspace+`"

[[watcher]]
name = "level"
interval = "60s"
command = "echo 1"
`)
	out.Reset()
	if code := runServiceCmd(context.Background(), []string{"edit", "svc", "--file", edited, "--yes"}, &out, log); code == 0 {
		t.Fatalf("editing a started service was accepted: %s", out.String())
	}

	out.Reset()
	if code := runServiceCmd(context.Background(), []string{"stop", "svc"}, &out, log); code != 0 {
		t.Fatalf("stop: %s", out.String())
	}
	out.Reset()
	if code := runServiceCmd(context.Background(), []string{"edit", "svc", "--file", edited, "--yes"}, &out, log); code != 0 {
		t.Fatalf("edit: %s", out.String())
	}

	store, err := session.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ss := store.(session.ServiceStore)
	info, err := ss.ServiceInfo("svc")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(newWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if info.Role != "coder" || info.Cwd != resolved || info.State != runner.StateStopped || info.NextWake != nil {
		t.Fatalf("after edit: role=%q cwd=%q state=%q next=%v", info.Role, info.Cwd, info.State, info.NextWake)
	}
	if !strings.Contains(info.Spec, `"goal":"edited"`) || !strings.Contains(info.Spec, `"level"`) {
		t.Fatalf("spec was not replaced: %s", info.Spec)
	}
	watchers, err := ss.Watchers(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, w := range watchers {
		names[w.Name] = true
		if w.NextRun != nil {
			t.Fatalf("watcher %q has a next run while stopped: %v", w.Name, w.NextRun)
		}
	}
	if len(watchers) != 1 || !names["level"] {
		t.Fatalf("watchers = %v, want only the edited probe", names)
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

// The HTTP path and `pai service create` share one rule set: a manifest is
// stored as written, a command watcher that was not pre-approved waits for a
// grant, and approval is a separate targeted write.
func TestServeBackendCreateService(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	workspace := t.TempDir()
	manifest := `
[service]
goal = "watch the river"
role = "devops"
workspace = "` + workspace + `"
report_interval = "1h"

[[watcher]]
name = "level"
interval = "60s"
command = "echo 1"
`
	store, err := session.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend := &serveBackend{store: store, cwd: workspace}

	// Without pre-approval the service is saved anyway; the first probe is what
	// asks. A timer runs no code, so it is never gated.
	pending, err := backend.CreateService("pending", manifest, false)
	if err != nil {
		t.Fatalf("create without approval: %v", err)
	}
	for _, w := range pending.Watchers {
		if w.Name == "level" && w.ApprovedAt != nil {
			t.Fatal("a command watcher was pre-approved without being asked")
		}
		if w.Name == config.ReportWatcherName && w.ApprovedAt == nil {
			t.Fatal("a timer runs no command, so it must not wait for a grant")
		}
	}

	view, err := backend.CreateService("svc", manifest, true)
	if err != nil {
		t.Fatal(err)
	}
	if view.Name != "svc" || view.Mode != session.ModeService || view.NextWake == nil {
		t.Fatalf("view = %+v", view)
	}
	names := map[string]bool{}
	for _, w := range view.Watchers {
		names[w.Name] = true
	}
	if !names["level"] || !names[config.ReportWatcherName] {
		t.Fatalf("watchers = %v, want level and %s", names, config.ReportWatcherName)
	}

	if _, err := backend.CreateService("svc", manifest, true); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate create: %v", err)
	}

	// A service's role lives in both the session row and the stored manifest; a
	// change must keep them in sync so the view and a restart agree.
	if err := backend.SetRole("svc", "coder"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	renamed, err := backend.ServiceView("svc")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Role != "coder" || !strings.Contains(renamed.Spec, `"role":"coder"`) {
		t.Fatalf("role change did not reach the manifest: role=%q spec=%s", renamed.Role, renamed.Spec)
	}

	got, err := backend.ServiceView("svc")
	if err != nil || got.Spec == "" || len(got.Watchers) != 2 {
		t.Fatalf("read back = %+v, %v", got, err)
	}
}
