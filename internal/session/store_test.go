package session

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Carudy/pai/internal/core"
)

// SetRole rewrites only the role, leaves history untouched, and reports a
// missing session. It runs against whichever backend the build selects.
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

// Truncate keeps a prefix and drops the tail; a later append continues after the
// kept turns, so the file/row layout stays consistent.
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
