package cli

import (
	"fmt"
	"strings"

	"github.com/Carudy/pai/internal/prompts"
)

// roleList renders the available roles for help text. Generated from
// prompts.RoleNames() so it can never go stale.
func roleList() string {
	names := prompts.RoleNames()
	if len(names) == 0 {
		return "<none>"
	}
	return strings.Join(names, " | ")
}

// chatFlagHelp documents the flags shared by `pai chat` and bare `pai`.
func chatFlagHelp() string {
	return `CHAT FLAGS
  -r, --role <name>     Role to run: ` + roleList() + ` (default from config)
  -m, --model <p:m>     Override the model for this run, as provider:model
  -s, --session <name>  Use or create a named session
      --attach <name>   Resume an existing session by name
  -C, --continue        Resume the most recent session (this directory first)
      --no-session      Do not persist this run, even if a session would apply
  -i, --inter           Enable multi-turn interactive chat
  -d, --debug           Enable debug logging
  -v, --version         Print version and exit
  -h, --help            Show this help`
}

func generalHelp() string {
	var b strings.Builder
	b.WriteString("PAI — Personal Agent Inside Terminal\n\n")
	b.WriteString("An LLM-powered CLI assistant for terminal and DevOps tasks.\n\n")
	b.WriteString("USAGE\n  pai [command] [flags] [input]\n\n")

	b.WriteString("COMMANDS\n")
	for _, c := range commands() {
		name := c.name
		if len(c.aliases) > 0 {
			name += " (" + strings.Join(c.aliases, ", ") + ")"
		}
		fmt.Fprintf(&b, "  %s%s\n", pad(name, 18), c.summary)
	}
	fmt.Fprintf(&b, "  %sShow help for a command\n", pad("help [command]", 18))

	b.WriteString("\nWith no command, pai chats: `pai <input>` is shorthand for `pai chat <input>`.\n\n")
	fmt.Fprintf(&b, "%s\n\n", chatFlagHelp())

	b.WriteString("EXAMPLES\n")
	b.WriteString("  pai \"list running docker containers\"\n")
	b.WriteString("  pai -r coder -m openai:gpt-4o \"add a --verbose flag\"\n")
	b.WriteString("  pai -s work \"check nginx status, then keep going\"\n")
	b.WriteString("  pai --attach work \"and now check disk usage\"\n")
	b.WriteString("  pai config set default_role coder\n")
	b.WriteString("  pai session ls\n")

	b.WriteString("\nCONFIG\n  PAI reads config and session data from its XDG directories. See README for details.\n")
	return b.String()
}

// chatHelp is the detailed help for the chat subcommand.
func chatHelp() string {
	return chatFlagHelp() + "\n\nEXAMPLES\n" +
		"  pai chat \"list running docker containers\"\n" +
		"  pai chat -i                    # interactive multi-turn session\n" +
		"  pai -s work --attach work      # continue a named session\n"
}

// pad right-pads s with spaces to at least n columns (never truncating).
func pad(s string, n int) string {
	if len(s) >= n {
		return s + " "
	}
	return s + strings.Repeat(" ", n-len(s))
}
