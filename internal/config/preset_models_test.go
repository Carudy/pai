package config

import (
	"testing"

	"github.com/BurntSushi/toml"
)

// preset_models is a provider:model suggestion list. Entries are trimmed,
// de-duplicated, and non provider:model values are dropped so a stray entry
// cannot become a broken dropdown suggestion.
func TestPresetModelsCleaned(t *testing.T) {
	var raw tomlConfig
	if err := toml.Unmarshal([]byte(`[app]
preset_models = [" deepseek:chat ", "", "openai:gpt-4o", "deepseek:chat", "noColon"]
`), &raw); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.fromTOML(&raw)

	want := []string{"deepseek:chat", "openai:gpt-4o"}
	if len(cfg.PresetModels) != len(want) {
		t.Fatalf("preset models = %v, want %v", cfg.PresetModels, want)
	}
	for i := range want {
		if cfg.PresetModels[i] != want[i] {
			t.Fatalf("preset models = %v, want %v", cfg.PresetModels, want)
		}
	}
}

// Omitting the key leaves the list empty (the built-in default is no presets).
func TestPresetModelsAbsent(t *testing.T) {
	var raw tomlConfig
	cfg := defaultConfig()
	cfg.fromTOML(&raw)
	if len(cfg.PresetModels) != 0 {
		t.Fatalf("preset models = %v, want none", cfg.PresetModels)
	}
	if cfg.DefaultModel != "deepseek:deepseek-v4-flash" {
		t.Fatalf("default = %q, want the built-in default", cfg.DefaultModel)
	}
}

// With no default_model configured, the first preset becomes the default, so a
// presets-only config still has a usable model.
func TestPresetModelsFirstBecomesDefault(t *testing.T) {
	var raw tomlConfig
	if err := toml.Unmarshal([]byte(`[app]
preset_models = ["llama:spark", "opencode:longcat"]
`), &raw); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.fromTOML(&raw)
	if cfg.DefaultModel != "llama:spark" {
		t.Fatalf("default = %q, want the first preset llama:spark", cfg.DefaultModel)
	}
}

// An explicit default_model always wins over the presets.
func TestExplicitDefaultModelBeatsPreset(t *testing.T) {
	var raw tomlConfig
	if err := toml.Unmarshal([]byte(`[app]
default_model = "openai:gpt-4o"
preset_models = ["llama:spark"]
`), &raw); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.fromTOML(&raw)
	if cfg.DefaultModel != "openai:gpt-4o" {
		t.Fatalf("default = %q, want the explicit openai:gpt-4o", cfg.DefaultModel)
	}
}
