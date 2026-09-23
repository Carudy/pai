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

// ContextConfig controls how the conversation is kept within the model's context
// window: shortening a single tool observation, and eliding old observations from
// the replayed history. All values come from the [context] section of config.toml
// with built-in defaults, so an omitted key keeps its default.
type ContextConfig struct {
	// Byte budgets for one observation fed back to the model.
	ExecLimit   int // execute/remote output
	SearchLimit int // websearch result

	// Lines kept from the head and tail of an observation that must be shortened.
	// The tail matters: errors and summaries land at the end of command output.
	HeadLines int
	TailLines int

	// Compaction of older history. The last KeepTurns messages are replayed
	// verbatim; tool observations older than that are elided to their header. No
	// elision happens until the conversation exceeds ElideAfterTurns messages, and
	// observations smaller than ElideMinBytes are left alone. ElideHeadLines is how
	// much of an elided observation survives.
	KeepTurns       int
	ElideAfterTurns int
	ElideMinBytes   int
	ElideHeadLines  int

	// SummarizeAfterTokens turns on the second compression layer: when the last
	// prompt exceeded this many tokens, the oldest turns are summarized by the
	// model into one message and only the last KeepTurns messages are kept. Zero
	// (the default) disables it — it costs an extra model call, so it is opt-in.
	SummarizeAfterTokens int
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
	Context struct {
		ExecLimit            int `toml:"exec_limit"`
		SearchLimit          int `toml:"search_limit"`
		HeadLines            int `toml:"head_lines"`
		TailLines            int `toml:"tail_lines"`
		KeepTurns            int `toml:"keep_turns"`
		ElideAfterTurns      int `toml:"elide_after_turns"`
		ElideMinBytes        int `toml:"elide_min_bytes"`
		ElideHeadLines       int `toml:"elide_head_lines"`
		SummarizeAfterTokens int `toml:"summarize_after_tokens"`
	} `toml:"context"`
	Tool struct {
		TavilyAPIKey string   `toml:"tavily_api_key"`
		TrustedCmds  []string `toml:"trusted_cmds"`
		// RemoteShell wraps remote commands as "<shell> -lc <cmd>" so a login
		// shell loads the remote PATH/env. Empty (the default) runs the command
		// through the remote login shell non-interactively, exactly as ssh does.
		RemoteShell string `toml:"remote_shell"`
	} `toml:"tool"`
	Session struct {
		Persist  bool `toml:"persist"`
		MaxTurns int  `toml:"max_turns"`
		// RecapTurns is a pointer so an explicit 0 (disable) is distinguishable
		// from an omitted key (use the built-in default).
		RecapTurns *int `toml:"recap_turns"`
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

	// RemoteShell, when set, runs remote commands through a login shell so the
	// remote PATH/env is loaded (e.g. nix profiles come from /etc/profile). A bare
	// name like "bash" is invoked as "bash -lc <cmd>"; a value containing a space
	// is used verbatim as the prefix. Empty means ssh's default (no wrapper).
	RemoteShell string

	// CustomPrompt is the user's override for the selected role's intro,
	// loaded from ~/.config/pai/prompts.toml.
	CustomPrompt CustomPrompt

	// Truncation and compaction for output fed back to the model.
	Context ContextConfig

	// SessionPersist makes every run persist to an auto-named session, even
	// without -s/--attach/--continue. SessionMaxTurns caps how many resumed
	// turns are replayed (0 = all). SessionRecapTurns is how many recent
	// exchanges are echoed back to the user when resuming (0 = no recap).
	SessionPersist    bool
	SessionMaxTurns   int
	SessionRecapTurns int

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
		DefaultModel:     "deepseek:deepseek-v4-flash",
		DefaultRole:      "devops",
		ProvidersConfigs: make(map[string]ProviderConfig),
		CustomPrompt:     CustomPrompt{},
		Context: ContextConfig{
			ExecLimit:       8000,
			SearchLimit:     8000,
			HeadLines:       80,
			TailLines:       40,
			KeepTurns:       8,
			ElideAfterTurns: 16,
			ElideMinBytes:   1000,
			ElideHeadLines:  8,
		},
		SessionRecapTurns: 3,
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
	cfg.RemoteShell = raw.Tool.RemoteShell
	cfg.SessionPersist = raw.Session.Persist
	cfg.SessionMaxTurns = raw.Session.MaxTurns
	if raw.Session.RecapTurns != nil {
		cfg.SessionRecapTurns = *raw.Session.RecapTurns
	}
	if raw.App.TruncateExecLimit > 0 {
		cfg.Context.ExecLimit = raw.App.TruncateExecLimit
	}
	if raw.App.TruncateSearchLimit > 0 {
		cfg.Context.SearchLimit = raw.App.TruncateSearchLimit
	}
	// [context] is canonical and overrides the legacy [app] keys above.
	ctx := raw.Context
	if ctx.ExecLimit > 0 {
		cfg.Context.ExecLimit = ctx.ExecLimit
	}
	if ctx.SearchLimit > 0 {
		cfg.Context.SearchLimit = ctx.SearchLimit
	}
	if ctx.HeadLines > 0 {
		cfg.Context.HeadLines = ctx.HeadLines
	}
	if ctx.TailLines > 0 {
		cfg.Context.TailLines = ctx.TailLines
	}
	if ctx.KeepTurns > 0 {
		cfg.Context.KeepTurns = ctx.KeepTurns
	}
	if ctx.ElideAfterTurns > 0 {
		cfg.Context.ElideAfterTurns = ctx.ElideAfterTurns
	}
	if ctx.ElideMinBytes > 0 {
		cfg.Context.ElideMinBytes = ctx.ElideMinBytes
	}
	if ctx.ElideHeadLines > 0 {
		cfg.Context.ElideHeadLines = ctx.ElideHeadLines
	}
	if ctx.SummarizeAfterTokens > 0 {
		cfg.Context.SummarizeAfterTokens = ctx.SummarizeAfterTokens
	}
}
