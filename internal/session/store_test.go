package session

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/core"
)

// SetRole rewrites only the role, leaves history untouched, and reports a
// missing session. It exercises the real store end to end.
func TestStoreSetRole(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.Create(Meta{Name: "s", Role: "devops"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append("s", core.Turn{Role: "user", Kind: "input", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRole("s", "coder"); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get("s")
	if err != nil {
		t.Fatal(err)
	}
	if got.Meta.Role != "coder" {
		t.Errorf("role = %q, want coder", got.Meta.Role)
	}
	if len(got.Turns) != 1 {
		t.Errorf("history changed: %d turns", len(got.Turns))
	}

	if err := store.SetRole("missing", "coder"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing session error = %v, want ErrNotFound", err)
	}
}

func TestStoreSetWorkingDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	oldDir, newDir := t.TempDir(), t.TempDir()
	if _, err := store.Create(Meta{Name: "workspace", Role: "coder", Model: "deepseek:test", Cwd: oldDir, Title: "keep title"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append("workspace",
		core.Turn{Role: "user", Kind: "input", Content: "hello"},
		core.Turn{Role: "assistant", Kind: "reply", Content: "world"}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Get("workspace")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Truncate(time.Second)
	if err := store.SetWorkingDir("workspace", newDir); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkingDir("missing", newDir); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session error = %v, want ErrNotFound", err)
	}
	if err := store.SetWorkingDir("bad/name", newDir); err == nil {
		t.Fatal("invalid name accepted")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open()
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("workspace")
	if err != nil {
		t.Fatal(err)
	}
	if got.Meta.Cwd != newDir {
		t.Fatalf("persisted cwd = %q, want %q", got.Meta.Cwd, newDir)
	}
	if got.Meta.UpdatedAt.Before(started) || got.Meta.UpdatedAt.After(time.Now()) || got.Meta.UpdatedAt.Before(before.Meta.UpdatedAt) {
		t.Fatalf("updated time = %v, before = %v", got.Meta.UpdatedAt, before.Meta.UpdatedAt)
	}
	got.Meta.Cwd, got.Meta.UpdatedAt = before.Meta.Cwd, before.Meta.UpdatedAt
	if !reflect.DeepEqual(got, before) {
		t.Fatalf("other session data changed: got %+v, want %+v", got, before)
	}
	if _, err := store.Latest(oldDir); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old workspace still matches: %v", err)
	}
	latest, err := store.Latest(newDir)
	if err != nil || latest.Meta.Name != "workspace" {
		t.Fatalf("new workspace lookup: %+v %v", latest, err)
	}
	metas, err := store.List()
	if err != nil || len(metas) != 1 || metas[0].Cwd != newDir || metas[0].Turns != 2 {
		t.Fatalf("list after update: %+v %v", metas, err)
	}
}

// Truncate keeps a prefix and drops the tail; a later append continues after the
// kept turns, so the stored layout stays consistent.
func TestStoreTruncate(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.Create(Meta{Name: "s", Role: "devops"}); err != nil {
		t.Fatal(err)
	}
	var turns []core.Turn
	for i := 0; i < 5; i++ {
		turns = append(turns, core.Turn{Role: "user", Kind: "input", Content: fmt.Sprintf("t%d", i)})
	}
	if err := store.Append("s", turns...); err != nil {
		t.Fatal(err)
	}

	if err := store.Truncate("s", 2); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("s")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 2 || got.Turns[0].Content != "t0" || got.Turns[1].Content != "t1" {
		t.Fatalf("turns = %+v, want t0,t1", got.Turns)
	}

	if err := store.Append("s", core.Turn{Role: "user", Kind: "input", Content: "t2b"}); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get("s")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 3 || got.Turns[2].Content != "t2b" {
		t.Fatalf("append after truncate = %+v, want t2b last", got.Turns)
	}

	if err := store.Truncate("missing", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing session error = %v, want ErrNotFound", err)
	}
}
