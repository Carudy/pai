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
// state, with no live runtime.
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
	); err != nil {
		t.Fatal(err)
	}

	view, err := b.Context("ctx")
	if err != nil {
		t.Fatal(err)
	}
	if view.Head == "" || view.Tail == "" {
		t.Fatalf("head/tail missing: head=%d tail=%d", len(view.Head), len(view.Tail))
	}
	if !strings.Contains(view.Head, "Working Dir: ") {
		t.Errorf("head should carry the terminal info block")
	}
	if len(view.Cards) != 2 {
		t.Fatalf("cards = %d, want 2", len(view.Cards))
	}
	if c := view.Cards[0]; c.Seq != 1 || c.Kind != core.KindInput || c.State != chat.StateSent || c.Sent != "hello" {
		t.Errorf("card 0 = %+v", c)
	}
	if c := view.Cards[1]; c.Seq != 2 || c.Kind != core.KindOutput || c.State != chat.StateSent {
		t.Errorf("card 1 = %+v", c)
	}

	if _, err := b.Context("missing"); err != runner.ErrNotFound {
		t.Errorf("missing session error = %v, want ErrNotFound", err)
	}
	if _, err := b.Context("bad/name"); err == nil {
		t.Error("an invalid session name should be rejected")
	}
}
