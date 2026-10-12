//go:build !filestore

package session

import (
	"testing"
	"time"
)

func TestSQLiteSetWorkingDirUpdatesTimestamp(t *testing.T) {
	store, err := openBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := store.(*sqliteStore)
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
