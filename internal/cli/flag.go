package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Carudy/pai/internal/hq"
)

const helpText = `PAI — Personal Agent Inside Terminal

An LLM-powered CLI assistant for shell commands, Q&A, and DevOps tasks.

USAGE
  pai [flags] <input>

FLAGS
  -a, --agent <name>   Agent mode: cmd | qa | devops (default from config)
  -i, --inter          Enable multi-turn interactive chat (for qa / devops)
  -d, --debug          Enable debug logging
  -v, --version        Print version and exit
  -h, --help           Show this help

AGENTS
  cmd      One-shot shell command generation with optional execution
  qa       Question answering (single-turn or interactive multi-turn TUI)
  devops   Autonomous reason–act–observe loop for multi-step sysadmin tasks

EXAMPLES
  pai "list running docker containers"
  pai -a cmd "sum the second column of data.csv"
  pai -a qa "explain Kubernetes pods"
  pai -a qa -i                        # Interactive question-answering
  pai -a devops "deploy my app to staging"
  pai -a devops -i                    # Interactive DevOps loop

CONFIG
  PAI looks for ~/.config/pai/config.yml. See README for details.
`

// GetFlags parses CLI args and returns the resolved CliFlags.
// A bool return indicates whether help was requested (caller should print
// helpText and exit cleanly).
func GetFlags(args []string) (hq.CliFlags, bool, error) {
	var flags hq.CliFlags

	// Print help for bare "-h", "--help", or no-arg invocation.
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Print(helpText)
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

	fs.StringVar(&flags.Agent, "agent", "", "pai's agent")
	fs.StringVar(&flags.Agent, "a", "", "pai's agent (shorthand)")

	if err := fs.Parse(args); err != nil {
		return flags, false, err
	}

	flags.Input = strings.Join(fs.Args(), " ")
	return flags, false, nil
}
