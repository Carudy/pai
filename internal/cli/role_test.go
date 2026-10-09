package cli

import (
	"testing"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/session"
)

// A resumed session keeps its saved role; an explicit -r overrides it.
func TestResolveSessionKeepsSavedRole(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	store, err := session.Open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(session.Meta{Name: "work", Role: "coder", Model: "test:model"}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	cfg := &config.UserConfig{DefaultRole: "devops", DefaultModel: "test:model"}
	resumed, _, err := resolveSession(cfg, CliFlags{Attach: "work"})
	if err != nil {
		t.Fatal(err)
	}
	resumed.Close()
	if cfg.DefaultRole != "coder" {
		t.Errorf("role = %q, want the session's saved coder", cfg.DefaultRole)
	}

	// -r wins over the saved role.
	override := &config.UserConfig{DefaultRole: "devops", DefaultModel: "test:model"}
	resumed, _, err = resolveSession(override, CliFlags{Attach: "work", Role: "devops"})
	if err != nil {
		t.Fatal(err)
	}
	resumed.Close()
	if override.DefaultRole != "devops" {
		t.Errorf("role = %q, want the explicit flag to win", override.DefaultRole)
	}
}
