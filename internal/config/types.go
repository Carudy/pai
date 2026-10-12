package config

import (
	"strings"
	"time"

	"github.com/Carudy/pai/internal/provider"
)

// ProviderConfig holds per-provider settings from the user config.
type ProviderConfig struct {
	APIKey  string `toml:"api_key"`
	BaseURL string `toml:"base_url"`
	// Protocol selects the wire format: "" or "chat" for the OpenAI-compatible
	// chat completions API, "responses" for OpenAI's Responses API (the
	// Codex-style /responses endpoint). Unknown values are rejected when a client
	// is created.
	Protocol string `toml:"protocol"`
	// Headers are extra HTTP request headers sent with every call, e.g. a custom
	// User-Agent or the x-opencode-session routing id. They override PAI's own
	// defaults. See internal/provider for how they are resolved.
	Headers map[string]string `toml:"headers"`
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
	PaperLimit  int // papersearch result, preserving complete records

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
		Streaming           *bool                    `toml:"streaming"`
		ReasoningEffort     provider.ReasoningEffort `toml:"reasoning"`
		Interactive         bool                     `toml:"interactive"`
		TruncateExecLimit   int                      `toml:"truncate_exec_limit"`
		TruncateSearchLimit int                      `toml:"truncate_search_limit"`
		// PresetModels are provider:model suggestions offered by frontends; a
		// suggestion list, never an allowlist.
		PresetModels []string `toml:"preset_models"`
	} `toml:"app"`
	Context struct {
		ExecLimit            int `toml:"exec_limit"`
		SearchLimit          int `toml:"search_limit"`
		PaperLimit           int `toml:"paper_limit"`
		HeadLines            int `toml:"head_lines"`
		TailLines            int `toml:"tail_lines"`
		KeepTurns            int `toml:"keep_turns"`
		ElideAfterTurns      int `toml:"elide_after_turns"`
		ElideMinBytes        int `toml:"elide_min_bytes"`
		ElideHeadLines       int `toml:"elide_head_lines"`
		SummarizeAfterTokens int `toml:"summarize_after_tokens"`
	} `toml:"context"`
	Tool struct {
		TavilyAPIKey      string   `toml:"tavily_api_key"`
		PaperSearchSource string   `toml:"papersearch_source"`
		PaperSearchEmail  string   `toml:"papersearch_email"`
		TrustedCmds       []string `toml:"trusted_cmds"`
		// TrustedPaths are directories inside which the file tools (edit, write,
		// and read when ConfirmRead is set) need no confirmation — the "trusted
		// working place". Empty trusts nothing.
		TrustedPaths []string `toml:"trusted_paths"`
		// ConfirmRead also requires confirmation to read a file outside
		// TrustedPaths. Off by default: a read is non-destructive, but sending a
		// file outside the workspace to the model is the main exfiltration path.
		ConfirmRead bool `toml:"confirm_read"`
		// RemoteShell wraps remote commands as "<shell> -lc <cmd>" so a login
		// shell loads the remote PATH/env. Empty (the default) runs the command
		// through the remote login shell non-interactively, exactly as ssh does.
		RemoteShell string `toml:"remote_shell"`
		// CmdTimeoutSeconds bounds one local command. 0 (the default) means no
		// timeout: with working cancellation, Ctrl+C is the control, and legitimate
		// long builds should not be cut off.
		CmdTimeoutSeconds int `toml:"cmd_timeout_seconds"`
	} `toml:"tool"`
	Session struct {
		Persist  bool `toml:"persist"`
		MaxTurns int  `toml:"max_turns"`
		// RecapTurns is a pointer so an explicit 0 (disable) is distinguishable
		// from an omitted key (use the built-in default).
		RecapTurns *int `toml:"recap_turns"`
		// RewindBackup is a pointer so an explicit false is distinguishable from an
		// omitted key (default on): before a rewind truncates a session, snapshot
		// its current turns into a rolling backup session.
		RewindBackup *bool `toml:"rewind_backup"`
	} `toml:"session"`
}

// UserConfig is PAI's configuration: everything here comes from config.toml,
// prompts.toml, or the environment.
//
// Runtime state for a single run — the LLM client, the logger, whether the
// session is interactive — deliberately does NOT live here; see role.Session.
// Keeping that separation is what lets config stay a near-leaf package.
type UserConfig struct {
	ProvidersConfigs  map[string]ProviderConfig
	DefaultModel      string
	DefaultRole       string
	Streaming         bool
	ReasoningEffort   provider.ReasoningEffort
	Interactive       bool
	TavilyAPIKey      string
	PaperSearchSource string
	// PaperSearchEmail optionally identifies Crossref requests for its polite pool.
	PaperSearchEmail string
	TrustedCmds      []string

	// TrustedPaths are directories inside which file edits/creates (and reads,
	// when ConfirmRead is set) are applied without a prompt. Empty means every
	// mutation is confirmed. See tool.IsTrustedPath.
	TrustedPaths []string
	// ConfirmRead requires a prompt to read a file outside TrustedPaths.
	ConfirmRead bool

	// RemoteShell, when set, runs remote commands through a login shell so the
	// remote PATH/env is loaded (e.g. nix profiles come from /etc/profile). A bare
	// name like "bash" is invoked as "bash -lc <cmd>"; a value containing a space
	// is used verbatim as the prefix. Empty means ssh's default (no wrapper).
	RemoteShell string

	// CmdTimeout bounds a single local command. Zero disables it (the default):
	// cancellation via Ctrl+C is the control, so long builds are not cut off.
	CmdTimeout time.Duration

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
	// SessionRewindBackup snapshots a session before a rewind truncates it, into a
	// rolling per-session backup, so nothing is permanently lost. Default on.
	SessionRewindBackup bool

	// Provider and Model are derived from DefaultModel's "provider:model" form.
	Provider string
	Model    string

	// PresetModels are extra provider:model suggestions for frontends (the web
	// model dropdown). They are suggestions only, never an allowlist, and are
	// cleaned to well-formed, de-duplicated provider:model entries on load.
	PresetModels []string
}

// Redacted returns a copy that is safe to log: provider API keys and the
// search key are masked. Debug output would otherwise print live credentials.
func (cfg *UserConfig) Redacted() *UserConfig {
	dup := *cfg
	if cfg.ProvidersConfigs != nil {
		dup.ProvidersConfigs = make(map[string]ProviderConfig, len(cfg.ProvidersConfigs))
		for name, pc := range cfg.ProvidersConfigs {
			pc.APIKey = MaskSecret(pc.APIKey)
			dup.ProvidersConfigs[name] = pc
		}
	}
	dup.TavilyAPIKey = MaskSecret(cfg.TavilyAPIKey)
	return &dup
}

// cleanModels trims each entry, drops empties and duplicates, and keeps only
// provider:model forms, so a stray entry never becomes a broken frontend
// suggestion. Order is preserved so the author controls the dropdown order.
func cleanModels(models []string) []string {
	clean := make([]string, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" || !strings.Contains(m, ":") || seen[m] {
			continue
		}
		seen[m] = true
		clean = append(clean, m)
	}
	return clean
}

// MaskSecret renders a secret as a presence hint, never its contents. Use it for
// logs and diagnostics, which must never carry a usable credential.
func MaskSecret(s string) string {
	if s == "" {
		return ""
	}
	return "****"
}

// SecretHint renders a secret for a human-facing display: empty stays empty,
// otherwise the last few characters are shown so two keys can be told apart.
func SecretHint(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= 4 {
		return "****"
	}
	return "****" + string(r[len(r)-4:])
}

func defaultConfig() *UserConfig {
	return &UserConfig{
		DefaultModel:      "deepseek:deepseek-v4-flash",
		DefaultRole:       "devops",
		PaperSearchSource: "arxiv",
		Streaming:         true,
		ProvidersConfigs:  make(map[string]ProviderConfig),
		CustomPrompt:      CustomPrompt{},
		Context: ContextConfig{
			ExecLimit:       8000,
			SearchLimit:     8000,
			PaperLimit:      16384,
			HeadLines:       80,
			TailLines:       40,
			KeepTurns:       8,
			ElideAfterTurns: 16,
			ElideMinBytes:   1000,
			ElideHeadLines:  8,
		},
		SessionRecapTurns:   3,
		SessionRewindBackup: true,
	}
}

// fromTOML copies parsed TOML values into the flat UserConfig. Empty strings are
// ignored so an omitted key keeps its built-in default.
func (cfg *UserConfig) fromTOML(raw *tomlConfig) {
	cfg.ProvidersConfigs = raw.Providers
	cfg.PresetModels = cleanModels(raw.App.PresetModels)
	switch {
	case raw.App.DefaultModel != "":
		cfg.DefaultModel = raw.App.DefaultModel
	case len(cfg.PresetModels) > 0:
		// No default_model configured: fall back to the first preset, so a
		// presets-only config still has a usable model instead of the built-in one.
		cfg.DefaultModel = cfg.PresetModels[0]
	}
	if raw.App.DefaultRole != "" {
		cfg.DefaultRole = raw.App.DefaultRole
	}
	if raw.App.Streaming != nil {
		cfg.Streaming = *raw.App.Streaming
	}
	cfg.ReasoningEffort = raw.App.ReasoningEffort
	cfg.Interactive = raw.App.Interactive
	cfg.TavilyAPIKey = raw.Tool.TavilyAPIKey
	if raw.Tool.PaperSearchSource != "" {
		cfg.PaperSearchSource = strings.ToLower(strings.TrimSpace(raw.Tool.PaperSearchSource))
	}
	cfg.PaperSearchEmail = strings.TrimSpace(raw.Tool.PaperSearchEmail)
	cfg.TrustedCmds = raw.Tool.TrustedCmds
	cfg.TrustedPaths = raw.Tool.TrustedPaths
	cfg.ConfirmRead = raw.Tool.ConfirmRead
	cfg.RemoteShell = raw.Tool.RemoteShell
	if raw.Tool.CmdTimeoutSeconds > 0 {
		cfg.CmdTimeout = time.Duration(raw.Tool.CmdTimeoutSeconds) * time.Second
	}
	cfg.SessionPersist = raw.Session.Persist
	cfg.SessionMaxTurns = raw.Session.MaxTurns
	if raw.Session.RecapTurns != nil {
		cfg.SessionRecapTurns = *raw.Session.RecapTurns
	}
	if raw.Session.RewindBackup != nil {
		cfg.SessionRewindBackup = *raw.Session.RewindBackup
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
	if ctx.PaperLimit > 0 {
		cfg.Context.PaperLimit = ctx.PaperLimit
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
