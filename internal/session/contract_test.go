package session

import (
	"errors"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/core"
)

// openContractStore opens the real store against an isolated data directory, so
// the Store contract is exercised end to end rather than through a fake.
func openContractStore(t *testing.T) Store {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	// Close is idempotent, so a test may close and reopen the same directory.
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

// assertTurns compares the fields that make up a turn. Times are compared by
// instant because SQLite stores whole seconds; an expected zero time means "the
// store stamps it at append time", so only an explicit timestamp is compared.
func assertTurns(t *testing.T, got, want []core.Turn) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d turns, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Role != w.Role || g.Kind != w.Kind || g.Content != w.Content {
			t.Errorf("turn %d = %+v, want %+v", i, g, w)
		}
		if !w.At.IsZero() && !g.At.Equal(w.At) {
			t.Errorf("turn %d time = %v, want %v", i, g.At, w.At)
		}
	}
}

func mustCreate(t *testing.T, store Store, meta Meta) {
	t.Helper()
	if _, err := store.Create(meta); err != nil {
		t.Fatalf("create %q: %v", meta.Name, err)
	}
}

// Every method documents ErrNotFound for a name it cannot find; this pins that
// they all return it rather than a driver error.
func TestStoreContractMissingSession(t *testing.T) {
	store := openContractStore(t)
	for name, op := range map[string]func() error{
		"Get":            func() error { _, err := store.Get("missing"); return err },
		"Append":         func() error { return store.Append("missing", core.Turn{Content: "x"}) },
		"SetModel":       func() error { return store.SetModel("missing", "m") },
		"SetRole":        func() error { return store.SetRole("missing", "coder") },
		"SetWorkingDir":  func() error { return store.SetWorkingDir("missing", "/d") },
		"SetOverride":    func() error { return store.SetOverride("missing", Override{Seq: 1, Action: core.OverrideDrop}) },
		"DeleteOverride": func() error { return store.DeleteOverride("missing", 1) },
		"Truncate":       func() error { return store.Truncate("missing", 1) },
		"Rename":         func() error { return store.Rename("missing", "other") },
		"Delete":         func() error { return store.Delete("missing") },
	} {
		if err := op(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s on a missing session = %v, want ErrNotFound", name, err)
		}
	}
	if _, err := store.Latest("/no/such/workspace"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Latest in an unknown workspace = %v, want ErrNotFound", err)
	}
	if _, err := store.Create(Meta{Name: "bad/name", Role: "devops"}); err == nil {
		t.Error("Create accepted an invalid name")
	}
	if err := store.Append("bad/name", core.Turn{Content: "x"}); err == nil {
		t.Error("Append accepted an invalid name")
	}
}

// Overrides ride alongside turns: one row per (session, seq), cleared when the
// turn they name goes away, and carried by a rename.
func TestStoreContractOverrides(t *testing.T) {
	store := openContractStore(t)
	mustCreate(t, store, Meta{Name: "one", Role: "devops"})
	var turns []core.Turn
	for i := 0; i < 3; i++ {
		turns = append(turns, core.Turn{Role: "user", Kind: "input", Content: string(rune('a' + i))})
	}
	if err := store.Append("one", turns...); err != nil {
		t.Fatal(err)
	}

	if err := store.SetOverride("one", Override{Seq: 1, Action: core.OverrideReplace, Content: "rewritten"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetOverride("one", Override{Seq: 2, Action: core.OverrideDrop, Content: "ignored"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("one")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Overrides) != 2 {
		t.Fatalf("overrides = %+v, want 2", got.Overrides)
	}
	if o := got.Overrides[1]; o.Action != core.OverrideReplace || o.Content != "rewritten" {
		t.Errorf("override 1 = %+v", o)
	}
	// A drop stores no text even if the caller passes some.
	if o := got.Overrides[2]; o.Action != core.OverrideDrop || o.Content != "" {
		t.Errorf("override 2 = %+v, want an empty drop", o)
	}

	// Setting the same turn again replaces rather than accumulates.
	if err := store.SetOverride("one", Override{Seq: 1, Action: core.OverrideDrop}); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Get("one")
	if len(got.Overrides) != 2 || got.Overrides[1].Action != core.OverrideDrop {
		t.Fatalf("upsert did not replace: %+v", got.Overrides)
	}

	// A turn that does not exist is ErrNotFound, not a foreign-key error.
	if err := store.SetOverride("one", Override{Seq: 99, Action: core.OverrideDrop}); !errors.Is(err, ErrNotFound) {
		t.Errorf("override on a missing turn = %v, want ErrNotFound", err)
	}
	if err := store.SetOverride("one", Override{Seq: 1, Action: "nope"}); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("invalid action = %v, want a validation error", err)
	}

	// Reset drops the row; resetting again is ErrNotFound.
	if err := store.DeleteOverride("one", 2); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteOverride("one", 2); !errors.Is(err, ErrNotFound) {
		t.Errorf("second reset = %v, want ErrNotFound", err)
	}

	// Truncating away a turn cascades its override; an override on a surviving turn
	// stays.
	if err := store.SetOverride("one", Override{Seq: 3, Action: core.OverrideDrop}); err != nil {
		t.Fatal(err)
	}
	if err := store.Truncate("one", 1); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Get("one")
	if len(got.Overrides) != 1 {
		t.Fatalf("after truncate overrides = %+v, want only the surviving turn", got.Overrides)
	}
	if _, ok := got.Overrides[3]; ok {
		t.Errorf("the dropped turn's override survived: %+v", got.Overrides)
	}

	// A surviving override follows a rename.
	if err := store.SetOverride("one", Override{Seq: 1, Action: core.OverrideReplace, Content: "kept"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Rename("one", "two"); err != nil {
		t.Fatal(err)
	}
	moved, err := store.Get("two")
	if err != nil {
		t.Fatal(err)
	}
	if o := moved.Overrides[1]; o.Action != core.OverrideReplace || o.Content != "kept" {
		t.Fatalf("override did not follow rename: %+v", moved.Overrides)
	}
}

// History and metadata must survive a close/reopen unchanged.
func TestStoreContractRoundTrip(t *testing.T) {
	store := openContractStore(t)
	mustCreate(t, store, Meta{Name: "one", Role: "devops", Model: "deepseek:test", Cwd: "/w", Title: "keep"})
	if _, err := store.Create(Meta{Name: "one", Role: "devops"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate Create = %v, want ErrExists", err)
	}

	at := time.Now().Truncate(time.Second).Add(-time.Minute)
	turns := []core.Turn{
		{Role: "user", Kind: "input", Content: "first", At: at},
		{Role: "assistant", Kind: "reply", Content: "second", At: at.Add(time.Second)},
	}
	if err := store.Append("one", turns...); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("one")
	if err != nil {
		t.Fatal(err)
	}
	assertTurns(t, got.Turns, turns)
	if got.Meta.Turns != 2 || got.Meta.Title != "keep" || got.Meta.Role != "devops" {
		t.Fatalf("metadata = %+v", got.Meta)
	}
	metas, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Turns != 2 || metas[0].Title != "keep" {
		t.Fatalf("list = %+v", metas)
	}

	// A new store over the same directory sees the same session.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := reopened.Get("one")
	if err != nil {
		t.Fatal(err)
	}
	assertTurns(t, again.Turns, turns)
	latest, err := reopened.Latest("/w")
	if err != nil || latest.Meta.Name != "one" {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
}

// Each metadata setter changes only its own field, leaving history alone.
func TestStoreContractMetadataSetters(t *testing.T) {
	store := openContractStore(t)
	mustCreate(t, store, Meta{Name: "one", Role: "devops", Model: "deepseek:test", Cwd: "/old", Title: "keep"})
	if err := store.Append("one", core.Turn{Role: "user", Kind: "input", Content: "hi"}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		set   func() error
		check func(Meta) bool
	}{
		{"SetModel", func() error { return store.SetModel("one", "openai:gpt") },
			func(m Meta) bool { return m.Model == "openai:gpt" }},
		{"SetRole", func() error { return store.SetRole("one", "coder") },
			func(m Meta) bool { return m.Role == "coder" }},
		{"SetWorkingDir", func() error { return store.SetWorkingDir("one", "/new") },
			func(m Meta) bool { return m.Cwd == "/new" }},
	} {
		if err := tc.set(); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got, err := store.Get("one")
		if err != nil {
			t.Fatal(err)
		}
		if !tc.check(got.Meta) || got.Meta.Cwd == "" || got.Meta.Title != "keep" || got.Meta.Role == "" {
			t.Errorf("%s: metadata = %+v", tc.name, got.Meta)
		}
		if len(got.Turns) != 1 {
			t.Errorf("%s changed history: %+v", tc.name, got.Turns)
		}
	}
	if _, err := store.Latest("/old"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the old workspace still matches: %v", err)
	}
}

// Truncate keeps a prefix, Rename moves everything, Delete removes everything.
func TestStoreContractTruncateRenameDelete(t *testing.T) {
	store := openContractStore(t)
	mustCreate(t, store, Meta{Name: "one", Role: "devops"})
	var turns []core.Turn
	for i := 0; i < 4; i++ {
		turns = append(turns, core.Turn{Role: "user", Kind: "input", Content: string(rune('a' + i))})
	}
	if err := store.Append("one", turns...); err != nil {
		t.Fatal(err)
	}

	if err := store.Truncate("one", 2); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("one")
	if err != nil {
		t.Fatal(err)
	}
	assertTurns(t, got.Turns, turns[:2])

	// A keep beyond the end is a no-op, not an error.
	if err := store.Truncate("one", 99); err != nil {
		t.Fatal(err)
	}
	// Appending after a truncate continues from the surviving prefix.
	if err := store.Append("one", core.Turn{Role: "user", Kind: "input", Content: "z"}); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get("one")
	if err != nil {
		t.Fatal(err)
	}
	assertTurns(t, got.Turns, append(append([]core.Turn{}, turns[:2]...), core.Turn{Role: "user", Kind: "input", Content: "z"}))

	// Rename carries metadata and history to the new name.
	if err := store.Rename("one", "two"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("one"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old name still present: %v", err)
	}
	renamed, err := store.Get("two")
	if err != nil || len(renamed.Turns) != 3 {
		t.Fatalf("renamed session = %+v, %v", renamed, err)
	}
	if err := store.Rename("two", "three"); err != nil {
		t.Fatal(err)
	}
	if err := store.Append("three", core.Turn{Role: "user", Kind: "input", Content: "after"}); err != nil {
		t.Fatal(err)
	}

	// Truncate clears everything at keep=0; Delete removes the session.
	if err := store.Truncate("three", 0); err != nil {
		t.Fatal(err)
	}
	emptied, err := store.Get("three")
	if err != nil || len(emptied.Turns) != 0 {
		t.Fatalf("truncate to zero = %+v, %v", emptied, err)
	}
	if err := store.Delete("three"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("three"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session still readable: %v", err)
	}
	metas, err := store.List()
	if err != nil || len(metas) != 0 {
		t.Fatalf("list after delete = %+v, %v", metas, err)
	}
}

// NewRecorder is how the loop persists turns, so the contract covers it too.
func TestStoreContractRecorder(t *testing.T) {
	store := openContractStore(t)
	mustCreate(t, store, Meta{Name: "one", Role: "devops"})
	rec := NewRecorder(store, "one")
	if err := rec.AppendTurn(core.Turn{Role: "user", Kind: "input", Content: "recorded"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("one")
	if err != nil {
		t.Fatal(err)
	}
	assertTurns(t, got.Turns, []core.Turn{{Role: "user", Kind: "input", Content: "recorded"}})
	if got.Turns[0].At.IsZero() {
		t.Error("a turn with no explicit time must be stamped at append time")
	}
}
