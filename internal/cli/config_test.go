package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/tui"
)

// preset_models is settable from the CLI, so a fresh config written by
// "pai config set" round-trips through LoadUserConfig and prints back.
func TestConfigSetPresetModels(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "pai"), 0700); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	log := tui.NewLogger(&out, false)
	if code := runConfig(context.Background(), []string{"set", "preset_models", "deepseek:a, openai:b"}, &out, log); code != 0 {
		t.Fatalf("set failed (%d): %s", code, out.String())
	}

	cfg, err := config.LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.PresetModels) != 2 || cfg.PresetModels[0] != "deepseek:a" || cfg.PresetModels[1] != "openai:b" {
		t.Fatalf("preset models = %v", cfg.PresetModels)
	}

	out.Reset()
	if code := runConfig(context.Background(), []string{"get", "preset_models"}, &out, log); code != 0 {
		t.Fatalf("get failed (%d): %s", code, out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "[deepseek:a, openai:b]" {
		t.Fatalf("get printed %q", got)
	}

	// A bare, non provider:model entry is rejected before it is written.
	out.Reset()
	if code := runConfig(context.Background(), []string{"set", "preset_models", "nope"}, &out, log); code == 0 {
		t.Fatal("accepted a non provider:model entry")
	}
}
