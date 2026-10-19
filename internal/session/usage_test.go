package session

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// The reported/estimated pair is what calibrates the token estimate everywhere else, so
// it has to round-trip through the store and survive a resumed read.
func TestSQLiteSetUsage(t *testing.T) {
	s := openSQLiteStore(t)
	if _, err := s.Create(Meta{Name: "usage", Role: "devops"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUsage("usage", 292_000, 340_000); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("usage")
	if err != nil {
		t.Fatal(err)
	}
	if got.Meta.PromptTokens != 292_000 || got.Meta.PromptEstimate != 340_000 {
		t.Fatalf("usage pair = %d/%d", got.Meta.PromptTokens, got.Meta.PromptEstimate)
	}
	if scale := got.Meta.TokenScale(); scale < 0.85 || scale > 0.86 {
		t.Errorf("TokenScale = %v, want ~0.859", scale)
	}

	// Accounting is not activity: it must not reorder the session list.
	before := got.Meta.UpdatedAt
	if err := s.SetUsage("usage", 1, 2); err != nil {
		t.Fatal(err)
	}
	again, err := s.Get("usage")
	if err != nil {
		t.Fatal(err)
	}
	if !again.Meta.UpdatedAt.Equal(before) {
		t.Errorf("SetUsage moved updated_at: %v -> %v", before, again.Meta.UpdatedAt)
	}

	if err := s.SetUsage("missing", 1, 2); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing session = %v, want ErrNotFound", err)
	}
}

// TokenScale is 1 until a request has reported, and clamped so one odd report cannot
// move a budget by more than a factor of two.
func TestMetaTokenScale(t *testing.T) {
	cases := []struct {
		name     string
		reported int
		estimate int
		want     float64
	}{
		{"nothing reported", 0, 0, 1},
		{"no estimate", 500, 0, 1},
		{"in range", 800, 1000, 0.8},
		{"clamped low", 10, 1000, 0.5},
		{"clamped high", 1000, 10, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Meta{PromptTokens: tc.reported, PromptEstimate: tc.estimate}
			if got := m.TokenScale(); got != tc.want {
				t.Errorf("TokenScale() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A database created before the accounting columns existed gains them on open, so an
// existing store keeps working: CREATE TABLE IF NOT EXISTS only covers a fresh file.
func TestSQLiteAddsAccountingColumnsToOldDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
CREATE TABLE sessions (
  name TEXT PRIMARY KEY, role TEXT NOT NULL, model TEXT NOT NULL, cwd TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
INSERT INTO sessions(name, role, model, cwd, created_at, updated_at)
VALUES('old', 'devops', 'deepseek:test', '/w', 1, 1);`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := openBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	got, err := store.Get("old")
	if err != nil {
		t.Fatalf("old database did not open: %v", err)
	}
	if got.Meta.PromptTokens != 0 || got.Meta.TokenScale() != 1 {
		t.Errorf("migrated session = %+v", got.Meta)
	}
	if err := store.SetUsage("old", 100, 50); err != nil {
		t.Errorf("SetUsage on a migrated database: %v", err)
	}
}
