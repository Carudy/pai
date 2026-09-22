package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/BurntSushi/toml"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/provider"
	"github.com/Carudy/pai/internal/tui"
)

// configKey describes one editable key in config.toml: where it lives and how
// to format/validate its value.
type configKey struct {
	section string
	key     string
	kind    string // model | role | string | bool | int | reasoning
	secret  bool
}

var configKeys = map[string]configKey{
	"default_model":         {section: "app", key: "default_model", kind: "model"},
	"default_role":          {section: "app", key: "default_role", kind: "role"},
	"streaming":             {section: "app", key: "streaming", kind: "bool"},
	"reasoning":             {section: "app", key: "reasoning", kind: "reasoning"},
	"interactive":           {section: "app", key: "interactive", kind: "bool"},
	"truncate_exec_limit":   {section: "app", key: "truncate_exec_limit", kind: "int"},
	"truncate_search_limit": {section: "app", key: "truncate_search_limit", kind: "int"},
	"session.persist":       {section: "session", key: "persist", kind: "bool"},
	"session.max_turns":     {section: "session", key: "max_turns", kind: "int"},
	"session.recap_turns":   {section: "session", key: "recap_turns", kind: "int"},
	"tavily_api_key":        {section: "tool", key: "tavily_api_key", kind: "string", secret: true},
	"remote_shell":          {section: "tool", key: "remote_shell", kind: "string"},
}

// configHelp is the detailed help for `pai config`.
func configHelp() string {
	return "SUBCOMMANDS\n" +
		"  list, ls           Show effective settings and their keys\n" +
		"  get <key>          Print one key's value\n" +
		"  set <key> <value>  Set a key in config.toml\n" +
		"  unset, rm <key>    Remove a key (reverts to the built-in default)\n" +
		"  path               Print the config.toml path\n\n" +
		"KEYS\n  " + strings.Join(sortedConfigKeys(), "\n  ")
}

// runConfig handles `pai config <list|get|set|unset|path>`.
func runConfig(_ context.Context, args []string, stdout io.Writer, log *tui.Logger) int {
	if len(args) == 0 {
		args = []string{"list"}
	}

	path := config.Path()
	if path == "" {
		log.Errorf("Error: cannot determine the config directory.\n")
		return 1
	}

	switch args[0] {
	case "list", "ls":
		return configList(path, stdout, log)
	case "get":
		if len(args) < 2 {
			log.Errorf("Usage: pai config get <key>\n")
			return 1
		}
		return configGet(path, args[1], stdout, log)
	case "set":
		if len(args) < 3 {
			log.Errorf("Usage: pai config set <key> <value>\n")
			return 1
		}
		return configSet(path, args[1], strings.Join(args[2:], " "), stdout, log)
	case "unset", "rm":
		if len(args) < 2 {
			log.Errorf("Usage: pai config unset <key>\n")
			return 1
		}
		return configUnset(path, args[1], stdout, log)
	case "path":
		fmt.Fprintln(stdout, path)
		return 0
	default:
		log.Errorf("Unknown config command %q; try: list | get | set | unset | path\n", args[0])
		return 1
	}
}

func configList(path string, stdout io.Writer, log *tui.Logger) int {
	cfg, err := config.LoadUserConfig()
	if err != nil {
		log.Errorf("Error loading config: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "# %s\n", path)
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, name := range sortedConfigKeys() {
		fmt.Fprintf(tw, "%s\t= %s\n", name, configValue(cfg, name, false))
	}
	return flush(tw)
}

func configGet(path, name string, stdout io.Writer, log *tui.Logger) int {
	if _, ok := configKeys[name]; !ok {
		return unknownConfigKey(name, log)
	}
	cfg, err := config.LoadUserConfig()
	if err != nil {
		log.Errorf("Error loading config: %v\n", err)
		return 1
	}
	// An explicit get reveals secrets; list masks them.
	fmt.Fprintln(stdout, configValue(cfg, name, true))
	return 0
}

func configSet(path, name, raw string, stdout io.Writer, log *tui.Logger) int {
	ck, ok := configKeys[name]
	if !ok {
		return unknownConfigKey(name, log)
	}

	value, err := formatConfigValue(ck.kind, raw)
	if err != nil {
		log.Errorf("Error: %s: %v\n", name, err)
		return 1
	}
	if err := validateConfigTOML(ck.key, value); err != nil {
		log.Errorf("Error: %s: %v\n", name, err)
		return 1
	}
	if err := config.SetScalar(path, ck.section, ck.key, value); err != nil {
		log.Errorf("Error writing config: %v\n", err)
		return 1
	}

	shown := raw
	if ck.secret {
		shown = maskSecret(raw)
	}
	fmt.Fprintf(stdout, "%s = %s\n", name, shown)
	return 0
}

func configUnset(path, name string, stdout io.Writer, log *tui.Logger) int {
	ck, ok := configKeys[name]
	if !ok {
		return unknownConfigKey(name, log)
	}
	if err := config.UnsetScalar(path, ck.section, ck.key); err != nil {
		log.Errorf("Error writing config: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Unset %s (reverts to the built-in default).\n", name)
	return 0
}

// configValue renders one key's effective value. Secrets are masked unless
// reveal is set.
func configValue(cfg *config.UserConfig, name string, reveal bool) string {
	switch name {
	case "default_model":
		return cfg.DefaultModel
	case "default_role":
		return cfg.DefaultRole
	case "streaming":
		return strconv.FormatBool(cfg.Streaming)
	case "reasoning":
		if cfg.ReasoningEffort == "" {
			return "none"
		}
		return string(cfg.ReasoningEffort)
	case "interactive":
		return strconv.FormatBool(cfg.Interactive)
	case "truncate_exec_limit":
		return strconv.Itoa(cfg.TruncateExecLimit)
	case "truncate_search_limit":
		return strconv.Itoa(cfg.TruncateSearchLimit)
	case "session.persist":
		return strconv.FormatBool(cfg.SessionPersist)
	case "session.max_turns":
		return strconv.Itoa(cfg.SessionMaxTurns)
	case "session.recap_turns":
		return strconv.Itoa(cfg.SessionRecapTurns)
	case "tavily_api_key":
		if reveal {
			return cfg.TavilyAPIKey
		}
		return maskSecret(cfg.TavilyAPIKey)
	case "remote_shell":
		return cfg.RemoteShell
	}
	return ""
}

// formatConfigValue validates raw and returns it as a TOML literal.
func formatConfigValue(kind, raw string) (string, error) {
	switch kind {
	case "model":
		if !strings.Contains(raw, ":") {
			return "", fmt.Errorf("expected provider:model (e.g. deepseek:deepseek-v4-flash)")
		}
		return strconv.Quote(raw), nil
	case "role":
		for _, n := range prompts.RoleNames() {
			if n == raw {
				return strconv.Quote(raw), nil
			}
		}
		return "", fmt.Errorf("unknown role %q; available: %s", raw, strings.Join(prompts.RoleNames(), ", "))
	case "string":
		return strconv.Quote(raw), nil
	case "bool":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return "", fmt.Errorf("expected true or false, got %q", raw)
		}
		return strconv.FormatBool(b), nil
	case "int":
		n, err := strconv.Atoi(raw)
		if err != nil {
			return "", fmt.Errorf("expected an integer, got %q", raw)
		}
		return strconv.Itoa(n), nil
	case "reasoning":
		switch provider.ReasoningEffort(raw) {
		case provider.ReasoningEffortLow, provider.ReasoningEffortMedium, provider.ReasoningEffortHigh, provider.ReasoningEffortNone:
			return strconv.Quote(raw), nil
		}
		return "", fmt.Errorf("expected one of low|medium|high|none, got %q", raw)
	}
	return "", fmt.Errorf("unsupported value type %q", kind)
}

func unknownConfigKey(name string, log *tui.Logger) int {
	if strings.Contains(name, "trusted_cmds") {
		log.Errorf("Error: trusted_cmds is an array and cannot be set from the CLI; edit %s by hand.\n", config.Path())
		return 1
	}
	log.Errorf("Unknown config key %q. Known keys:\n  %s\n", name, strings.Join(sortedConfigKeys(), "\n  "))
	return 1
}

func sortedConfigKeys() []string {
	names := make([]string, 0, len(configKeys))
	for n := range configKeys {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// maskSecret renders a secret as a hint: "" stays empty, otherwise the last few
// characters are shown.
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= 4 {
		return "****"
	}
	return "****" + string(r[len(r)-4:])
}

// validateConfigTOML is a sanity check that a key/value pair parses as TOML.
// Guards against a quote escaping the value we are about to write.
func validateConfigTOML(key, value string) error {
	var probe map[string]any
	return toml.Unmarshal([]byte(key+" = "+value+"\n"), &probe)
}
