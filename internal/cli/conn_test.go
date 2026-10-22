package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Carudy/pai/internal/config"
)

// conn defaults live in config.toml, but a flag or environment variable must win.
func TestConnTargetPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "pai"), 0700); err != nil {
		t.Fatal(err)
	}
	path := config.Path()
	for key, value := range map[string]string{
		"url":    `"http://config:9384"`,
		"token":  `"config-token"`,
		"origin": `"https://config.example"`,
	} {
		if err := config.SetScalar(path, "conn", key, value); err != nil {
			t.Fatal(err)
		}
	}

	url, tok, org, err := connTarget("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if url != "http://config:9384" || tok != "config-token" || org != "https://config.example" {
		t.Fatalf("config values = %q %q %q", url, tok, org)
	}

	t.Setenv("PAI_SERVE_URL", "http://env:1")
	t.Setenv("PAI_SERVE_TOKEN", "env-token")
	t.Setenv("PAI_SERVE_ORIGIN", "https://env.example")
	url, tok, org, err = connTarget("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if url != "http://env:1" || tok != "env-token" || org != "https://env.example" {
		t.Fatalf("environment did not win over config: %q %q %q", url, tok, org)
	}

	url, tok, org, err = connTarget("http://flag:2", "flag-token", "https://flag.example")
	if err != nil {
		t.Fatal(err)
	}
	if url != "http://flag:2" || tok != "flag-token" || org != "https://flag.example" {
		t.Fatalf("flags did not win over environment: %q %q %q", url, tok, org)
	}
}

// An unset token is normal for a loopback server, so it must not become an error.
func TestConnTargetAllowsEmptyToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "pai"), 0700); err != nil {
		t.Fatal(err)
	}
	url, tok, org, err := connTarget("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if url != "" || tok != "" || org != "" {
		t.Fatalf("empty defaults = %q %q %q", url, tok, org)
	}
}

// A broken config is reported instead of silently ignored: these values are
// config, and every other command surfaces the same error.
func TestConnTargetReportsBrokenConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "pai"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte("conn = [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := connTarget("http://127.0.0.1:1", "token", "https://example.com"); err == nil {
		t.Fatal("a broken config was ignored")
	}
}
