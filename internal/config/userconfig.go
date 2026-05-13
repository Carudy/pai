package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Carudy/pai/internal/llm"
	"gopkg.in/yaml.v3"
)

// configDir returns the path to ~/.config/pai, the directory where pai
// stores its config files.
func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home dir: %w", err)
	}
	return filepath.Join(home, ".config", "pai"), nil
}

func LoadUserConfig() (*UserConfig, error) {
	cfgDir, err := configDir()
	if err != nil {
		return nil, err
	}
	cfg := defaultConfig()

	// ── config.yml (basic settings) ───────────────────────────────────────
	if err := loadYAML(filepath.Join(cfgDir, "config.yml"), cfg); err != nil {
		return nil, err
	}

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

// loadYAML reads a YAML file into dst. Missing files are silently ignored;
// other read/parse errors are returned.
func loadYAML(path string, dst any) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("failed to parse %s: %w", path, err)
	}
	return nil
}

// LoadCustomPrompt reads ~/.config/pai/prompts.yml and returns the custom
// prompt text for agentName, or "" if the file is missing or the agent has no
// entry. Call this after the session agent has been resolved.
func LoadCustomPrompt(agentName string) (CustomPrompt, error) {
	cfgDir, err := configDir()
	if err != nil {
		return CustomPrompt{}, err
	}
	path := filepath.Join(cfgDir, "prompts.yml")

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return CustomPrompt{}, nil
	}
	if err != nil {
		return CustomPrompt{}, fmt.Errorf("failed to read %s: %w", path, err)
	}
	var entries map[string]CustomPrompt
	if err := yaml.Unmarshal(data, &entries); err != nil {
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
