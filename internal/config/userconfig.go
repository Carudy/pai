package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/Carudy/pai/internal/paths"
	"github.com/Carudy/pai/internal/provider"
)

func LoadUserConfig() (*UserConfig, error) {
	cfgDir := paths.ConfigDir()
	if cfgDir == "" {
		return nil, fmt.Errorf("cannot determine the config directory (no home directory or XDG_CONFIG_HOME)")
	}
	cfg := defaultConfig()

	// ── config.toml (basic settings) ───────────────────────────────────────
	var raw tomlConfig
	if err := loadTOML(filepath.Join(cfgDir, "config.toml"), &raw); err != nil {
		return nil, err
	}
	cfg.fromTOML(&raw)

	// ── env API keys ──────────────────────────────────────────────────────
	mergeEnvAPIKeys(cfg)

	// ── resolve provider & model from "provider:model" string ────────────
	parts := strings.SplitN(cfg.DefaultModel, ":", 2)
	cfg.Provider = parts[0]
	if len(parts) == 2 {
		cfg.Model = parts[1]
	}

	return cfg, nil
}

// loadTOML reads a TOML file into dst. Missing files are silently ignored.
func loadTOML(path string, dst any) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("failed to parse %s: %w", path, err)
	}
	return nil
}

// LoadCustomPrompt reads ~/.config/pai/prompts.toml and returns the custom
// prompt text for roleName.
func LoadCustomPrompt(roleName string) (CustomPrompt, error) {
	cfgDir := paths.ConfigDir()
	if cfgDir == "" {
		return CustomPrompt{}, nil
	}
	path := filepath.Join(cfgDir, "prompts.toml")

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return CustomPrompt{}, nil
	}
	if err != nil {
		return CustomPrompt{}, fmt.Errorf("failed to read %s: %w", path, err)
	}
	var entries map[string]CustomPrompt
	if err := toml.Unmarshal(data, &entries); err != nil {
		return CustomPrompt{}, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	return entries[roleName], nil
}

func mergeEnvAPIKeys(cfg *UserConfig) {
	if cfg.ProvidersConfigs == nil {
		cfg.ProvidersConfigs = make(map[string]ProviderConfig)
	}
	for _, provider := range provider.BuiltinProviders {
		pc, exists := cfg.ProvidersConfigs[provider]
		if !exists {
			pc = ProviderConfig{}
		}
		if pc.APIKey != "" {
			continue
		}
		envKey := strings.ToUpper(provider) + "_API_KEY"
		if val := os.Getenv(envKey); val != "" {
			pc.APIKey = val
			cfg.ProvidersConfigs[provider] = pc
		}
	}
}
