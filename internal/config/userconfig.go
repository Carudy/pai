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
	cfg.SetModel(cfg.DefaultModel)

	return cfg, nil
}

// Path returns the location of the user's config.toml.
func Path() string {
	dir := paths.ConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.toml")
}

// SetModel sets DefaultModel and re-derives the Provider/Model pair from its
// "provider:model" form. A bare value with no colon is treated as the provider
// name with an empty model.
func (cfg *UserConfig) SetModel(s string) {
	cfg.DefaultModel = s
	providerName, model, found := strings.Cut(s, ":")
	cfg.Provider = providerName
	if found {
		cfg.Model = model
	} else {
		cfg.Model = ""
	}
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

// roleExtra is the parsed shape of a role's entry in role-extra.toml.
type roleExtra struct {
	Text string `toml:"text"`
}

// LoadRoleExtra reads ~/.config/pai/role-extra.toml and returns the extra intro
// text for roleName (empty when unset). The text is appended to the role's intro.
func LoadRoleExtra(roleName string) (string, error) {
	cfgDir := paths.ConfigDir()
	if cfgDir == "" {
		return "", nil
	}
	path := filepath.Join(cfgDir, "role-extra.toml")

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", path, err)
	}
	var entries map[string]roleExtra
	if err := toml.Unmarshal(data, &entries); err != nil {
		return "", fmt.Errorf("failed to parse %s: %w", path, err)
	}
	return entries[roleName].Text, nil
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
