package config

import (
	"github.com/Carudy/pai/internal/hq"
	"github.com/Carudy/pai/internal/llm"
)

// ProviderConfig holds per-provider settings from the user config.
type ProviderConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
}

type CustomPrompt struct {
	Additional bool   `yaml:"additional"`
	Prompt     string `yaml:"prompt"`
}

type UserConfig struct {
	// --- from ~/.config/pai/config.yml ---
	ProvidersConfigs map[string]ProviderConfig `yaml:"providers"`
	DefaultModel     string                    `yaml:"default_model"`
	DefaultAgent     string                    `yaml:"default_agent"`
	Streaming        bool                      `yaml:"streaming"`
	ReasoningEffort  llm.ReasoningEffort       `yaml:"reasoning"`

	// --- from ~/.config/pai/prompts.yml ---
	CustomPrompt CustomPrompt

	// --- resolved at runtime, not from config files ---
	Provider string
	Model    string
	Clients  map[string]llm.Provider
	Flags    *hq.CliFlags
}

func defaultConfig() *UserConfig {
	return &UserConfig{
		DefaultModel:     "deepseek:deepseek-v4-flash",
		DefaultAgent:     "devops",
		ProvidersConfigs: make(map[string]ProviderConfig),
		Clients:          make(map[string]llm.Provider),
		CustomPrompt:     CustomPrompt{},
	}
}
