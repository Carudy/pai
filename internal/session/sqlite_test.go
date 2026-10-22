package session

import (
	"errors"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/core"
)

func openSQLiteStore(t *testing.T) *sqliteStore {
	t.Helper()
	store, err := openBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store.(*sqliteStore)
}

// Essential is a per-turn, revocable flag persisted in its own column, independent
// of the turn's override surgery.
func TestSetEssentialRoundTrip(t *testing.T) {
	s := openSQLiteStore(t)
	if _, err := s.Create(Meta{Name: "ess", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("ess", core.Turn{Role: "user", Kind: core.KindInput, Content: "keep"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEssential("ess", 1, true); err != nil {
		t.Fatal(err)
	}
	sess, err := s.Get("ess")
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Turns) != 1 || !sess.Turns[0].Essential {
		t.Fatalf("essential not persisted: %+v", sess.Turns)
	}
	if err := s.SetEssential("ess", 1, false); err != nil {
		t.Fatal(err)
	}
	if sess, err = s.Get("ess"); err != nil || sess.Turns[0].Essential {
		t.Fatalf("essential not revoked: %+v %v", sess, err)
	}
	if err := s.SetEssential("ess", 99, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing turn should be ErrNotFound: %v", err)
	}
}

func TestSQLiteSetWorkingDirUpdatesTimestamp(t *testing.T) {
	s := openSQLiteStore(t)
	if _, err := s.Create(Meta{Name: "workspace", Cwd: "/old"}); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour).Unix()
	if _, err := s.db.Exec(`UPDATE sessions SET updated_at = ? WHERE name = ?`, oldTime, "workspace"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkingDir("workspace", "/new"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("workspace")
	if err != nil {
		t.Fatal(err)
	}
	if got.Meta.Cwd != "/new" || got.Meta.UpdatedAt.Unix() <= oldTime {
		t.Fatalf("workspace metadata not updated: %+v", got.Meta)
	}
}

// Store promises ErrExists for a rename onto a taken name; without the explicit
// check the UNIQUE constraint surfaces as a raw driver error instead.
func TestSQLiteRenameContract(t *testing.T) {
	s := openSQLiteStore(t)
	for _, name := range []string{"one", "two"} {
		if _, err := s.Create(Meta{Name: name, Role: "devops"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Rename("one", "two"); !errors.Is(err, ErrExists) {
		t.Fatalf("collision error = %v, want ErrExists", err)
	}
	if err := s.Rename("missing", "three"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing error = %v, want ErrNotFound", err)
	}
	if err := s.Rename("one", "bad/name"); err == nil {
		t.Fatal("invalid name accepted")
	}
	// A rejected rename must leave both sessions intact.
	if err := s.Rename("one", "one"); err != nil {
		t.Fatalf("self-rename: %v", err)
	}
	if err := s.Rename("one", "renamed"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("renamed")
	if err != nil || got.Meta.Name != "renamed" {
		t.Fatalf("rename did not persist: %+v %v", got, err)
	}
	if _, err := s.Get("one"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old name still present: %v", err)
	}
}

// Timestamps have one-second resolution, so ties are common. List and Latest
// must break them the same way, or ordering depends on row insertion order and
// can flip between runs.
func TestSQLiteOrderingIsDeterministic(t *testing.T) {
	s := openSQLiteStore(t)
	for _, name := range []string{"b", "a", "c"} {
		if _, err := s.Create(Meta{Name: name, Role: "devops"}); err != nil {
			t.Fatal(err)
		}
	}
	metas, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 3 {
		t.Fatalf("list = %+v", metas)
	}
	for i := 1; i < len(metas); i++ {
		prev, cur := metas[i-1], metas[i]
		if prev.UpdatedAt.Before(cur.UpdatedAt) {
			t.Fatalf("not newest-first: %v before %v", prev.UpdatedAt, cur.UpdatedAt)
		}
		if prev.UpdatedAt.Equal(cur.UpdatedAt) && prev.Name > cur.Name {
			t.Fatalf("equal timestamps not name-ordered: %q then %q", prev.Name, cur.Name)
		}
	}
	latest, err := s.Latest("")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Meta.Name != metas[0].Name {
		t.Fatalf("Latest = %q, List[0] = %q", latest.Meta.Name, metas[0].Name)
	}
}
