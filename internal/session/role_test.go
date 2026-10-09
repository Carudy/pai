package session

import (
	"errors"
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
