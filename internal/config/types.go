package config

import (
	"github.com/Carudy/pai/internal/hq"
	"github.com/Carudy/pai/internal/llm"
	"github.com/Carudy/pai/internal/tool"
)

// ProviderConfig holds per-provider settings from the user config.
type ProviderConfig struct {
	APIKey  string `toml:"api_key"`
	BaseURL string `toml:"base_url"`
}

type CustomPrompt struct {
	Additional bool   `toml:"additional"`
	Prompt     string `toml:"prompt"`
}

// tomlConfig mirrors the structure of ~/.config/pai/config.toml.
type tomlConfig struct {
	Providers map[string]ProviderConfig `toml:"providers"`
	App       struct {
		DefaultModel    string              `toml:"default_model"`
		DefaultAgent    string              `toml:"default_agent"`
		Streaming       bool                `toml:"streaming"`
		ReasoningEffort llm.ReasoningEffort `toml:"reasoning"`
		Interactive     bool                `toml:"interactive"`
	} `toml:"app"`
	Security struct {
		TrustedCmds []string `toml:"trusted_cmds"`
	} `toml:"security"`
}

// UserConfig is the flat runtime representation (populated from tomlConfig).
type UserConfig struct {
	ProvidersConfigs map[string]ProviderConfig
	DefaultModel     string
	DefaultAgent     string
	Streaming        bool
	ReasoningEffort  llm.ReasoningEffort
	Interactive      bool
	TrustedCmds      []string

	// --- from ~/.config/pai/prompts.yml ---
	CustomPrompt CustomPrompt

	// --- resolved at runtime, not from config files ---
	Provider      string
	Model         string
	Clients       map[string]llm.Provider
	Flags         *hq.CliFlags
	Logger        *hq.Logger
	RemoteManager *tool.RemoteManager
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

// fromTOML copies parsed TOML values into the flat UserConfig.
func (cfg *UserConfig) fromTOML(raw *tomlConfig) {
	cfg.ProvidersConfigs = raw.Providers
	cfg.DefaultModel = raw.App.DefaultModel
	cfg.DefaultAgent = raw.App.DefaultAgent
	cfg.Streaming = raw.App.Streaming
	cfg.ReasoningEffort = raw.App.ReasoningEffort
	cfg.Interactive = raw.App.Interactive
	cfg.TrustedCmds = raw.Security.TrustedCmds
}
