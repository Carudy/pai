package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/Carudy/pai/internal/llm"
)

// ConfigDir returns the path to ~/.config/pai.
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home dir: %w", err)
	}
	return filepath.Join(home, ".config", "pai"), nil
}

func LoadUserConfig() (*UserConfig, error) {
	cfgDir, err := ConfigDir()
	if err != nil {
		return nil, err
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
// prompt text for agentName.
func LoadCustomPrompt(agentName string) (CustomPrompt, error) {
	cfgDir, err := ConfigDir()
	if err != nil {
		return CustomPrompt{}, err
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
	return entries[agentName], nil
}

func mergeEnvAPIKeys(cfg *UserConfig) {
	if cfg.ProvidersConfigs == nil {
		cfg.ProvidersConfigs = make(map[string]ProviderConfig)
	}
	for _, provider := range llm.BuiltinProviders {
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
