package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/runner"
	"github.com/Carudy/pai/internal/session"
)

// The context view reports the role's head and tail, one card per stored turn
// with its send state, and the compaction settings in effect — all from persisted
// state, with no live runtime. Cards come back windowed, so a long transcript is
// never returned whole.
func TestServeBackendContext(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, err := session.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cwd, _ := os.Getwd()
	b := &serveBackend{store: store, cwd: cwd}
	if _, err := b.Create("ctx", cwd); err != nil {
		t.Fatal(err)
	}
	if err := store.Append("ctx",
		core.Turn{Role: "user", Kind: core.KindInput, Content: "hello"},
		core.Turn{Role: "assistant", Kind: core.KindOutput, Content: "hi"},
		core.Turn{Role: "user", Kind: core.KindToolResult, Content: "obs"},
		core.Turn{Role: "assistant", Kind: core.KindOutput, Content: "done"},
	); err != nil {
		t.Fatal(err)
	}

	// A limit past the end returns the whole list and reports the full length.
	view, err := b.Context("ctx", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if view.Head == "" || view.Tail == "" {
		t.Fatalf("head/tail missing: head=%d tail=%d", len(view.Head), len(view.Tail))
	}
	if !strings.Contains(view.Head, "Working Dir: ") {
		t.Errorf("head should carry the terminal info block")
	}
	if view.Total != 4 || view.Offset != 0 || len(view.Cards) != 4 {
		t.Fatalf("full window = total %d offset %d cards %d, want 4/0/4", view.Total, view.Offset, len(view.Cards))
	}
	if c := view.Cards[0]; c.Seq != 1 || c.Kind != core.KindInput || c.State != chat.StateSent || c.Sent != "hello" {
		t.Errorf("card 0 = %+v", c)
	}
	if c := view.Cards[1]; c.Seq != 2 || c.Kind != core.KindOutput || c.State != chat.StateSent {
		t.Errorf("card 1 = %+v", c)
	}

	// A tail window returns its slice; Seq keeps the original numbering, so a
	// client can walk back without the cards shifting under it.
	tail, err := b.Context("ctx", 2, 100)
	if err != nil {
		t.Fatal(err)
	}
	if tail.Total != 4 || tail.Offset != 2 || len(tail.Cards) != 2 || tail.Cards[0].Seq != 3 {
		t.Fatalf("tail window = %+v", tail)
	}
	if head, err := b.Context("ctx", 0, 2); err != nil || head.Offset != 0 || len(head.Cards) != 2 || head.Cards[1].Seq != 2 {
		t.Fatalf("head window = %+v, %v", head, err)
	}

	// An offset past the end is an empty window at the end, not an error.
	if end, err := b.Context("ctx", 99, 10); err != nil || end.Offset != 4 || len(end.Cards) != 0 || end.Total != 4 {
		t.Fatalf("past-the-end window = %+v, %v", end, err)
	}

	for _, tc := range []struct {
		name        string
		offset, lim int
	}{
		{"negative offset", -1, 10},
		{"zero limit", 0, 0},
		{"limit over the cap", 0, 201},
		{"huge offset", 1000001, 10},
	} {
		if _, err := b.Context("ctx", tc.offset, tc.lim); err == nil {
			t.Errorf("%s should be rejected", tc.name)
		}
	}

	if _, err := b.Context("missing", 0, 10); err != runner.ErrNotFound {
		t.Errorf("missing session error = %v, want ErrNotFound", err)
	}
	if _, err := b.Context("bad/name", 0, 10); err == nil {
		t.Error("an invalid session name should be rejected")
	}
}
