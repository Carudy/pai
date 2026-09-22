package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Carudy/pai/internal/tui"
)

// command is one CLI subcommand. The table in commands() is the single source
// of truth for dispatch, per-command help, and the top-level command list.
type command struct {
	name    string
	aliases []string
	summary string
	usage   string
	help    func() string // detailed help body; may be generated
	run     func(ctx context.Context, args []string, stdout io.Writer, log *tui.Logger) int
}

func commands() []command {
	return []command{
		{
			name:    "chat",
			summary: "Talk to a role (the default action when no command is given)",
			usage:   "pai chat [flags] [input]",
			help:    chatHelp,
			run:     runChat,
		},
		{
			name:    "session",
			aliases: []string{"sess"},
			summary: "List, inspect, rename, and delete saved sessions",
			usage:   "pai session <list|show|rm|rename> [args]",
			help:    sessionHelp,
			run:     runSession,
		},
		{
			name:    "config",
			aliases: []string{"cfg"},
			summary: "Inspect and edit configuration (config.toml)",
			usage:   "pai config <list|get|set|unset|path> [args]",
			help:    configHelp,
			run:     runConfig,
		},
		{
			name:    "role",
			aliases: []string{"roles"},
			summary: "List and inspect available roles",
			usage:   "pai role <list|show> [name]",
			help:    roleHelp,
			run:     runRole,
		},
	}
}

// findCommand resolves a name or alias to a command, or nil if unknown.
func findCommand(name string) *command {
	for _, c := range commands() {
		if c.name == name {
			return &c
		}
		for _, a := range c.aliases {
			if a == name {
				return &c
			}
		}
	}
	return nil
}

// globals holds the flags that are meaningful for every invocation and may
// appear before a subcommand.
type globals struct {
	debug   bool
	version bool
	help    bool
}

// splitGlobals peels leading global flags so they may precede a subcommand
// (e.g. `pai -d session ls`). Peeling stops at the first token that is not a
// known global flag, so flags that take a value stay with their subcommand.
func splitGlobals(args []string) (g globals, rest []string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d", "--debug":
			g.debug = true
		case "-v", "--version":
			g.version = true
		case "-h", "--help":
			g.help = true
		default:
			return g, args[i:]
		}
	}
	return g, nil
}

// Run is the main entry point for the PAI CLI. It dispatches to a subcommand,
// or — when the first non-flag argument is not a known command — treats the
// whole invocation as a chat. Returns a process exit code.
func Run(ctx context.Context, stdout io.Writer, args []string) int {
	global, rest := splitGlobals(args)
	log := tui.NewLogger(stdout, global.debug)

	if global.version {
		fmt.Fprintf(stdout, "PAI version: %s\n", Version)
		return 0
	}

	// `pai help [command]`
	if len(rest) > 0 && rest[0] == "help" {
		return printHelp(stdout, rest[1:])
	}

	if len(rest) > 0 {
		if cmd := findCommand(rest[0]); cmd != nil {
			if global.help {
				printCommandHelp(stdout, cmd)
				return 0
			}
			return cmd.run(ctx, rest[1:], stdout, log)
		}
	}

	// No subcommand: `-h` shows general help, anything else is a chat.
	if global.help {
		printHelp(stdout, nil)
		return 0
	}
	return runChat(ctx, args, stdout, log)
}

// printHelp prints general help, or a single command's help when topics[0]
// names one. Returns an exit code.
func printHelp(stdout io.Writer, topics []string) int {
	if len(topics) == 0 {
		fmt.Fprint(stdout, generalHelp())
		return 0
	}
	cmd := findCommand(topics[0])
	if cmd == nil {
		fmt.Fprintf(stdout, "Unknown help topic %q. Commands: %s\n", topics[0], commandNames())
		return 1
	}
	printCommandHelp(stdout, cmd)
	return 0
}

func printCommandHelp(stdout io.Writer, cmd *command) {
	fmt.Fprintf(stdout, "%s — %s\n\nUSAGE\n  %s\n", cmd.name, cmd.summary, cmd.usage)
	if cmd.help != nil {
		fmt.Fprintf(stdout, "\n%s\n", strings.TrimRight(cmd.help(), "\n"))
	}
}

// commandNames renders "chat, config, role, session" for error messages.
func commandNames() string {
	names := make([]string, 0, len(commands()))
	for _, c := range commands() {
		names = append(names, c.name)
	}
	return strings.Join(names, ", ")
}
