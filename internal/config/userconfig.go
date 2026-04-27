package config

import (
	"fmt"
	"os"

	// "pai/internal/agent"
	"path/filepath"
	"strings"

	anyllm "github.com/mozilla-ai/any-llm-go"
	"gopkg.in/yaml.v3"
)

var SupportedProviders = []string{"openai", "deepseek", "anthropic", "mistral"}

type UserConfig struct {
	APIKeys      map[string]string `yaml:"api_keys"`
	DefaultModel string            `yaml:"default_model"`
	DefaultAgent string            `yaml:"default_agent"`
	Prompts      map[string]string `yaml:"prompts"`

	Provider string
	Model    string
	Clients  map[string]*anyllm.Provider
}

func LoadUserConfig() (*UserConfig, error) {
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

	// env API keys
	mergeEnvAPIKeys(cfg)

	// split provider & model
	_split := strings.Split(cfg.DefaultModel, ":")
	_provider := _split[0]
	_model := strings.Join(_split[1:], "")
	cfg.Provider = _provider
	cfg.Model = _model

	return cfg, nil
}

func defaultConfig() *UserConfig {
	return &UserConfig{
		DefaultModel: "deepseek:deepseek-v4-flash",
		DefaultAgent: "cmd",
		APIKeys:      make(map[string]string),
		Prompts:      make(map[string]string),
		Clients:      make(map[string]*anyllm.Provider),
	}
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
