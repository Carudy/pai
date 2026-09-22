package config

import "github.com/Carudy/pai/internal/provider"

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
		DefaultModel        string                   `toml:"default_model"`
		DefaultRole         string                   `toml:"default_role"`
		Streaming           bool                     `toml:"streaming"`
		ReasoningEffort     provider.ReasoningEffort `toml:"reasoning"`
		Interactive         bool                     `toml:"interactive"`
		TruncateExecLimit   int                      `toml:"truncate_exec_limit"`
		TruncateSearchLimit int                      `toml:"truncate_search_limit"`
	} `toml:"app"`
	Tool struct {
		TavilyAPIKey string   `toml:"tavily_api_key"`
		TrustedCmds  []string `toml:"trusted_cmds"`
	} `toml:"tool"`
	Session struct {
		Persist  bool `toml:"persist"`
		MaxTurns int  `toml:"max_turns"`
	} `toml:"session"`
}

// UserConfig is PAI's configuration: everything here comes from config.toml,
// prompts.toml, or the environment.
//
// Runtime state for a single run — the LLM client, the logger, whether the
// session is interactive — deliberately does NOT live here; see role.Session.
// Keeping that separation is what lets config stay a near-leaf package.
type UserConfig struct {
	ProvidersConfigs map[string]ProviderConfig
	DefaultModel     string
	DefaultRole      string
	Streaming        bool
	ReasoningEffort  provider.ReasoningEffort
	Interactive      bool
	TavilyAPIKey     string
	TrustedCmds      []string

	// CustomPrompt is the user's override for the selected role's intro,
	// loaded from ~/.config/pai/prompts.toml.
	CustomPrompt CustomPrompt

	// Truncation limits for output fed back to the model (0 = built-in default).
	TruncateExecLimit   int
	TruncateSearchLimit int

	// SessionPersist makes every run persist to an auto-named session, even
	// without -s/--attach/--continue. SessionMaxTurns caps how many resumed
	// turns are replayed (0 = all).
	SessionPersist  bool
	SessionMaxTurns int

	// Provider and Model are derived from DefaultModel's "provider:model" form.
	Provider string
	Model    string
}

// Redacted returns a copy that is safe to log: provider API keys and the
// search key are masked. Debug output would otherwise print live credentials.
func (cfg *UserConfig) Redacted() *UserConfig {
	dup := *cfg
	if cfg.ProvidersConfigs != nil {
		dup.ProvidersConfigs = make(map[string]ProviderConfig, len(cfg.ProvidersConfigs))
		for name, pc := range cfg.ProvidersConfigs {
			pc.APIKey = maskSecret(pc.APIKey)
			dup.ProvidersConfigs[name] = pc
		}
	}
	dup.TavilyAPIKey = maskSecret(cfg.TavilyAPIKey)
	return &dup
}

// maskSecret renders a secret as a presence hint, never its contents.
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	return "****"
}

func defaultConfig() *UserConfig {
	return &UserConfig{
		DefaultModel:        "deepseek:deepseek-v4-flash",
		DefaultRole:         "devops",
		ProvidersConfigs:    make(map[string]ProviderConfig),
		CustomPrompt:        CustomPrompt{},
		TruncateExecLimit:   8000,
		TruncateSearchLimit: 8000,
	}
}

// fromTOML copies parsed TOML values into the flat UserConfig. Empty strings are
// ignored so an omitted key keeps its built-in default.
func (cfg *UserConfig) fromTOML(raw *tomlConfig) {
	cfg.ProvidersConfigs = raw.Providers
	if raw.App.DefaultModel != "" {
		cfg.DefaultModel = raw.App.DefaultModel
	}
	if raw.App.DefaultRole != "" {
		cfg.DefaultRole = raw.App.DefaultRole
	}
	cfg.Streaming = raw.App.Streaming
	cfg.ReasoningEffort = raw.App.ReasoningEffort
	cfg.Interactive = raw.App.Interactive
	cfg.TavilyAPIKey = raw.Tool.TavilyAPIKey
	cfg.TrustedCmds = raw.Tool.TrustedCmds
	cfg.SessionPersist = raw.Session.Persist
	cfg.SessionMaxTurns = raw.Session.MaxTurns
	if raw.App.TruncateExecLimit > 0 {
		cfg.TruncateExecLimit = raw.App.TruncateExecLimit
	}
	if raw.App.TruncateSearchLimit > 0 {
		cfg.TruncateSearchLimit = raw.App.TruncateSearchLimit
	}
}
