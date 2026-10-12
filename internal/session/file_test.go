//go:build filestore

package session

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestFileSetWorkingDirPreservesRawHistory(t *testing.T) {
	store, err := openBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := store.(*fileStore)
	oldTime := time.Now().Add(-time.Hour).UTC()
	meta := Meta{Name: "workspace", Role: "coder", Model: "deepseek:test", Cwd: "/old", Title: "keep title", CreatedAt: oldTime, UpdatedAt: oldTime}
	header, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve formatting and even an interrupted final append, not just parsed turns.
	history := []byte("{\"role\": \"user\", \"kind\": \"input\", \"content\": \"keep\"}\n{\"role\":\"assistant\",\"content\":\"")
	if err := os.WriteFile(s.path(meta.Name), append(append(header, '\n'), history...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(s.path(meta.Name), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkingDir(meta.Name, "/new"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.path(meta.Name))
	if err != nil {
		t.Fatal(err)
	}
	newHeader, newHistory, _ := bytes.Cut(raw, []byte{'\n'})
	if !bytes.Equal(newHistory, history) {
		t.Fatalf("history bytes changed: %q", newHistory)
	}
	var got Meta
	if err := json.Unmarshal(newHeader, &got); err != nil {
		t.Fatal(err)
	}
	if got.Cwd != "/new" || !got.UpdatedAt.After(oldTime) {
		t.Fatalf("workspace metadata not updated: %+v", got)
	}
	got.Cwd, got.UpdatedAt = meta.Cwd, meta.UpdatedAt
	if got != meta {
		t.Fatalf("other metadata changed: %+v, want %+v", got, meta)
	}
}
