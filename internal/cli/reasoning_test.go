package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// ReasoningDefault reads the global [app] reasoning setting, the fallback for
// sessions with no in-memory override.
func TestServeBackendReasoningDefault(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	if err := os.MkdirAll(filepath.Join(cfgDir, "pai"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "pai", "config.toml"), []byte("[app]\nreasoning = \"low\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := &serveBackend{}
	if got, err := b.ReasoningDefault(); err != nil || got != "low" {
		t.Fatalf("ReasoningDefault = (%q, %v), want low", got, err)
	}
}

// With no [app] reasoning configured, the default is "none".
func TestServeBackendReasoningDefaultUnset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := &serveBackend{}
	if got, err := b.ReasoningDefault(); err != nil || got != "none" {
		t.Fatalf("ReasoningDefault = (%q, %v), want none", got, err)
	}
}
