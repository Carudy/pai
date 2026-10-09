package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAddTrustedCmds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgPath := filepath.Join(dir, "pai", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[tool]\ntrusted_cmds = [\"ls\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// A duplicate is ignored; a new name is appended.
	if err := AddTrustedCmds("git", "ls"); err != nil {
		t.Fatal(err)
	}
	var raw tomlConfig
	if err := loadTOML(cfgPath, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Tool.TrustedCmds) != 2 || raw.Tool.TrustedCmds[0] != "ls" || raw.Tool.TrustedCmds[1] != "git" {
		t.Fatalf("trusted_cmds = %v, want [ls git]", raw.Tool.TrustedCmds)
	}

	// Adding only names already present writes nothing new.
	if err := AddTrustedCmds("git"); err != nil {
		t.Fatal(err)
	}
	if err := loadTOML(cfgPath, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Tool.TrustedCmds) != 2 {
		t.Errorf("trusted_cmds changed on a no-op add: %v", raw.Tool.TrustedCmds)
	}
}
