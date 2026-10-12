package config

import (
	"os"
	"path/filepath"
	"testing"
)

// LoadRoleExtra reads a role's extra intro text from role-extra.toml. A role with
// no entry yields "".
func TestLoadRoleExtra(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	paiDir := filepath.Join(dir, "pai")
	if err := os.MkdirAll(paiDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paiDir, "role-extra.toml"), []byte("[devops]\ntext = \"be careful\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got, err := LoadRoleExtra("devops"); err != nil || got != "be careful" {
		t.Fatalf("devops: got (%q, %v), want %q", got, err, "be careful")
	}
	if got, err := LoadRoleExtra("coder"); err != nil || got != "" {
		t.Fatalf("coder: got (%q, %v), want empty", got, err)
	}
}

// A missing role-extra.toml yields no extra text rather than an error.
func TestLoadRoleExtraMissingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got, err := LoadRoleExtra("devops"); err != nil || got != "" {
		t.Fatalf("got (%q, %v), want empty", got, err)
	}
}
