package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/session"
	"github.com/Carudy/pai/internal/tui"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A group is created from a manifest over existing services, listed, inspected,
// and deleted without touching the sessions themselves.
func TestGroupCreateShowRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	workspace := t.TempDir()
	dir := t.TempDir()

	for _, name := range []string{"coord", "hydro"} {
		path := writeFile(t, dir, name+".toml", `
[service]
goal = "watch the river"
role = "devops"
workspace = "`+workspace+`"

[[watcher]]
name = "tick"
interval = "45s"
`)
		var out bytes.Buffer
		log := tui.NewLogger(&out, false)
		if code := runServiceCmd(context.Background(), []string{"create", name, "--file", path}, &out, log); code != 0 {
			t.Fatalf("create %s: %s", name, out.String())
		}
	}

	groupPath := writeFile(t, dir, "hydro.toml", `
name = "hydro"

[group]
lead = "coord"
goal = "keep the river gates safe during maintenance"

[[member]]
name = "coord"

[[member]]
name = "hydro"
`)
	var out bytes.Buffer
	log := tui.NewLogger(&out, false)
	if code := runGroupCmd(context.Background(), []string{"create", "--file", groupPath}, &out, log); code != 0 {
		t.Fatalf("group create: %s", out.String())
	}
	if !strings.Contains(out.String(), "created group hydro with 2 member(s)") {
		t.Fatalf("create output = %q", out.String())
	}

	out.Reset()
	if code := runGroupCmd(context.Background(), []string{"list"}, &out, log); code != 0 {
		t.Fatalf("group list: %s", out.String())
	}
	if !strings.Contains(out.String(), "hydro") || !strings.Contains(out.String(), "coord") {
		t.Fatalf("list output = %q", out.String())
	}

	out.Reset()
	if code := runGroupCmd(context.Background(), []string{"show", "hydro"}, &out, log); code != 0 {
		t.Fatalf("group show: %s", out.String())
	}
	for _, want := range []string{"lead:", "coord", "members:     2/8", "goal:        keep the river gates safe"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("show output is missing %q:\n%s", want, out.String())
		}
	}

	// The lead is the address for `group:name` mail, so it cannot simply leave.
	out.Reset()
	if code := runGroupCmd(context.Background(), []string{"rm-member", "hydro", "coord"}, &out, log); code == 0 {
		t.Fatalf("the lead was removed: %s", out.String())
	}

	// The board is rendered with the group, so an operator sees stuck work.
	boardStore, err := session.Open()
	if err != nil {
		t.Fatal(err)
	}
	gs, ok := boardStore.(session.GroupStore)
	if !ok {
		boardStore.Close()
		t.Fatal("store does not expose groups")
	}
	group, found, err := gs.GroupByName("hydro")
	if err != nil || !found {
		boardStore.Close()
		t.Fatalf("group = %v %v", found, err)
	}
	ts, ok := boardStore.(session.TaskStore)
	if !ok {
		boardStore.Close()
		t.Fatal("store does not expose the board")
	}
	if _, err := ts.CreateTask(core.Task{GroupID: group.ID, Title: "check the gate"}); err != nil {
		boardStore.Close()
		t.Fatal(err)
	}
	boardStore.Close()

	out.Reset()
	if code := runGroupCmd(context.Background(), []string{"show", "hydro"}, &out, log); code != 0 {
		t.Fatalf("group show with a board: %s", out.String())
	}
	if !strings.Contains(out.String(), "board:") || !strings.Contains(out.String(), "check the gate") {
		t.Fatalf("board missing from show:\n%s", out.String())
	}

	// A chat session cannot join: a group is made of resident services.
	store, err := session.Open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(session.Meta{Name: "chatty", Cwd: workspace}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()
	out.Reset()
	if code := runGroupCmd(context.Background(), []string{"add", "hydro", "chatty"}, &out, log); code == 0 {
		t.Fatalf("a chat session joined a group: %s", out.String())
	}

	// The lead is the group's address, so neither delete verb may remove it and
	// leave the group pointing at nobody.
	for _, cmd := range [][]string{{"rm", "coord"}, {"session", "rm", "coord"}} {
		out.Reset()
		code := 1
		if cmd[0] == "session" {
			code = runSession(context.Background(), cmd[1:], &out, log)
		} else {
			code = runServiceCmd(context.Background(), cmd, &out, log)
		}
		if code == 0 {
			t.Fatalf("`%s` deleted the lead: %s", strings.Join(cmd, " "), out.String())
		}
		if !strings.Contains(out.String(), "leads group") {
			t.Fatalf("`%s` refusal does not name the group: %q", strings.Join(cmd, " "), out.String())
		}
	}

	// Deleting the group keeps its member sessions.
	out.Reset()
	if code := runGroupCmd(context.Background(), []string{"rm", "hydro"}, &out, log); code != 0 {
		t.Fatalf("group rm: %s", out.String())
	}
	out.Reset()
	if code := runServiceCmd(context.Background(), []string{"list"}, &out, log); code != 0 {
		t.Fatalf("service list: %s", out.String())
	}
	if !strings.Contains(out.String(), "coord") || !strings.Contains(out.String(), "hydro") {
		t.Fatalf("deleting the group removed its sessions:\n%s", out.String())
	}
}
