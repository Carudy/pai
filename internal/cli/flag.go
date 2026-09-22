package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Carudy/pai/internal/prompts"
)

// CliFlags holds all CLI flag values parsed from os.Args.
type CliFlags struct {
	Version  bool
	Debug    bool
	Inter    bool
	Role     string
	Session  string
	Attach   string
	Continue bool
	Input    string
}

// helpText is generated rather than hardcoded so the role list can't go stale.
func helpText() string {
	return `PAI — Personal Agent Inside Terminal

An LLM-powered CLI assistant for terminal and DevOps tasks.

USAGE
  pai [flags] <input>

FLAGS
  -r, --role <name>    Role to run: ` + strings.Join(prompts.RoleNames(), " | ") + ` (default from config)
  -s, --session <name> Use or create a named session
      --attach <name>  Resume an existing session
  -C, --continue       Resume the most recent session for this directory
  -i, --inter          Enable multi-turn interactive chat
  -d, --debug          Enable debug logging
  -v, --version        Print version and exit
  -h, --help           Show this help

SESSIONS
  pai session list                List saved sessions
  pai session show <name>         Show a session's details and recent turns
  pai session rm <name>           Delete a session
  pai session rename <old> <new>  Rename a session

EXAMPLES
  pai "list running docker containers"
  pai -r coder "add a --verbose flag to the CLI"
  pai -s work "check nginx status, then keep going"
  pai --attach work "and now check disk usage"
  pai -i                                  # Interactive session

CONFIG
  PAI reads config and session data from its XDG directories. See README for details.
`
}

// GetFlags parses CLI args and returns the resolved CliFlags.
// A bool return indicates whether help was requested (caller should print
// helpText and exit cleanly).
func GetFlags(args []string) (CliFlags, bool, error) {
	var flags CliFlags

	// Print help for bare "-h", "--help", or no-arg invocation.
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Print(helpText())
			return flags, true, nil
		}
	}

	fs := flag.NewFlagSet("pai", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.BoolVar(&flags.Version, "version", false, "pai's version")
	fs.BoolVar(&flags.Version, "v", false, "pai's version (shorthand)")

	fs.BoolVar(&flags.Debug, "debug", false, "Enable debug mode")
	fs.BoolVar(&flags.Debug, "d", false, "Enable debug mode (shorthand)")

	fs.BoolVar(&flags.Inter, "inter", false, "Enable multi-turn chat")
	fs.BoolVar(&flags.Inter, "i", false, "Enable multi-turn chat (shorthand)")

	fs.StringVar(&flags.Role, "role", "", "role to run")
	fs.StringVar(&flags.Role, "r", "", "role to run (shorthand)")

	fs.StringVar(&flags.Session, "session", "", "use or create a named session")
	fs.StringVar(&flags.Session, "s", "", "use or create a named session (shorthand)")

	fs.StringVar(&flags.Attach, "attach", "", "resume an existing session by name")

	fs.BoolVar(&flags.Continue, "continue", false, "resume the most recent session for this directory")
	fs.BoolVar(&flags.Continue, "C", false, "resume the most recent session (shorthand)")

	if err := fs.Parse(args); err != nil {
		return flags, false, err
	}

	flags.Input = strings.Join(fs.Args(), " ")
	return flags, false, nil
}
