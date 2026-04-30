package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Carudy/pai/internal/llm"
	"gopkg.in/yaml.v3"
)

type UserConfig struct {
	// --- from ~/.config/pai/config.yml ---
	APIKeys      map[string]string `yaml:"api_keys"`
	DefaultModel string            `yaml:"default_model"`
	DefaultAgent string            `yaml:"default_agent"`
	Streaming    bool              `yaml:"streaming"`
	Reasoning    bool              `yaml:"reasoning"`

	// --- resolved at runtime (lazy, after agent is known) ---
	// CustomPrompt holds the user's prompt override for the current session's
	// agent, loaded on demand from ~/.config/pai/prompts.yml via LoadCustomPrompt.
	// Empty string means use the built-in embedded prompt.
	CustomPrompt string `yaml:"-"`

	// --- resolved at runtime, not from config files ---
	Provider string
	Model    string
	Clients  map[string]llm.Provider
	Flags    *CliFlags
}

func defaultConfig() *UserConfig {
	return &UserConfig{
		DefaultModel: "deepseek:deepseek-v4-flash",
		DefaultAgent: "devops",
		APIKeys:      make(map[string]string),
		Clients:      make(map[string]llm.Provider),
	}
}

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
func LoadCustomPrompt(agentName string) (string, error) {
	cfgDir, err := configDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(cfgDir, "prompts.yml")

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", path, err)
	}
	var entries map[string]struct {
		Prompt string `yaml:"prompt"`
	}
	if err := yaml.Unmarshal(data, &entries); err != nil {
		return "", fmt.Errorf("failed to parse %s: %w", path, err)
	}
	return strings.TrimSpace(entries[agentName].Prompt), nil
}

func mergeEnvAPIKeys(cfg *UserConfig) {
	if cfg.APIKeys == nil {
		cfg.APIKeys = make(map[string]string)
	}
	for _, provider := range SupportedProviders {
		if _, exists := cfg.APIKeys[provider]; exists {
			continue
		}
		envKey := strings.ToUpper(provider) + "_API_KEY"
		if val := os.Getenv(envKey); val != "" {
			cfg.APIKeys[provider] = val
		}
	}
}
