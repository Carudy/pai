package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAddTrustedPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgPath := filepath.Join(dir, "pai", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[tool]\ntrusted_paths = [\"/a\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := AddTrustedPath("/b"); err != nil {
		t.Fatal(err)
	}
	var raw tomlConfig
	if err := loadTOML(cfgPath, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Tool.TrustedPaths) != 2 || raw.Tool.TrustedPaths[1] != "/b" {
		t.Fatalf("trusted_paths = %v, want [/a /b]", raw.Tool.TrustedPaths)
	}

	// A duplicate directory writes nothing new.
	if err := AddTrustedPath("/b"); err != nil {
		t.Fatal(err)
	}
	if err := loadTOML(cfgPath, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Tool.TrustedPaths) != 2 {
		t.Errorf("trusted_paths changed on a no-op add: %v", raw.Tool.TrustedPaths)
	}
}
