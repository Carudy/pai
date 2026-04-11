package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"pai/internal/prompt"
)

var SupportedProviders = []string{"openai", "deepseek", "anthropic"}

type Config struct {
	Provider     string            `yaml:"provider"`
	APIKeys      map[string]string `yaml:"api_keys"`
	DefaultModel string            `yaml:"default_model"`
	AskPrompt    string            `yaml:"ask_prompt"`
	CmdPrompt    string            `yaml:"cmd_prompt"`
}

func Load() (*Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home dir: %w", err)
	}

	cfg := defaultConfig()
	configPath := filepath.Join(homeDir, ".config", "pai", "config.yml")

	data, err := os.ReadFile(configPath)
	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	mergeEnvAPIKeys(cfg)

	return cfg, nil
}

func defaultConfig() *Config {
	return &Config{
		Provider:     "deepseek",
		APIKeys:      make(map[string]string),
		DefaultModel: "deepseek-chat",
		AskPrompt:    prompt.DefaultAskPrompt,
		CmdPrompt:    prompt.DefaultCommandPrompt,
	}
}

func mergeEnvAPIKeys(cfg *Config) {
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
