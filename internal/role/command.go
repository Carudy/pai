package role

import (
	"fmt"
	"strings"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/provider"
)

// cmdOutcome is what a line of user input turned out to be.
type cmdOutcome int

const (
	cmdMessage cmdOutcome = iota // ordinary input: hand it to the model
	cmdHandled                   // a command ran; keep prompting
	cmdExit                      // end the session
)

// cmdCtx is the state an in-session command may read or change. The loop owns
// the pointed-to value and re-reads it after each command, so /role and /new can
// swap the prompt and clear the conversation.
type cmdCtx struct {
	rt      *Runtime
	cfg     *config.UserConfig
	rp      *chat.RolePrompt
	history []provider.Message
	session string // "" = ephemeral
}

func (c *cmdCtx) output(text string) { c.rt.Observer.Output(text) }

func (c *cmdCtx) notice(format string, a ...any) {
	c.rt.Observer.Notice(fmt.Sprintf(format, a...))
}

// cmdSpec describes one in-session command.
type cmdSpec struct {
	name    string
	aliases []string
	usage   string // the exact grammar, shown on a usage error
	summary string
	minArgs int
	maxArgs int                  // -1 = unbounded
	check   func([]string) error // extra argument validation, beyond arity
	run     func(*cmdCtx, []string) (cmdOutcome, string)
}

// commandSpecs is the single source of truth for dispatch and /help, so the two
// can never disagree. Order here is the order /help prints. It is filled in by
// init because the table's closures refer back to it (via helpText), which the
// compiler would otherwise reject as an initialization cycle.
var commandSpecs []cmdSpec

func init() {
	commandSpecs = []cmdSpec{
		{
			name: "help", aliases: []string{"h", "?"},
			usage:   "/help",
			summary: "List the available commands",
			run: func(c *cmdCtx, _ []string) (cmdOutcome, string) {
				c.output(helpText())
				return cmdHandled, ""
			},
		},
		{
			name: "exit", aliases: []string{"quit", "q"},
			usage:   "/exit",
			summary: "End this session",
			run: func(*cmdCtx, []string) (cmdOutcome, string) {
				return cmdExit, ""
			},
		},
		{
			name: "info", aliases: []string{"status"},
			usage:   "/info",
			summary: "Show the session, role, model, and turn count",
			run:     runInfo,
		},
		{
			name:    "tools",
			usage:   "/tools",
			summary: "List the active role's tools",
			run:     runTools,
		},
		{
			name:    "role",
			usage:   "/role [name]",
			summary: "Show or switch the active role",
			maxArgs: 1,
			check:   checkRoleName,
			run:     runRole,
		},
		{
			name:    "new",
			usage:   "/new [name]",
			summary: "Start a fresh conversation, optionally named",
			maxArgs: 1,
			run:     runNew,
		},
		{
			name:    "rename",
			usage:   "/rename <name>",
			summary: "Name or rename this conversation, saving it",
			minArgs: 1,
			maxArgs: 1,
			run:     runRename,
		},
	}
}

// lookupCommand resolves a name or alias.
func lookupCommand(name string) *cmdSpec {
	for i := range commandSpecs {
		s := &commandSpecs[i]
		if s.name == name {
			return s
		}
		for _, a := range s.aliases {
			if a == name {
				return s
			}
		}
	}
	return nil
}

// splitCommand reports whether line is a command invocation and, if so, its
// (lower-cased) name and arguments. A line that does not begin with a single
// slash is ordinary input.
func splitCommand(line string) (name string, args []string, isCmd bool) {
	if !strings.HasPrefix(line, "/") || strings.HasPrefix(line, "//") {
		return "", nil, false
	}
	fields := strings.Fields(strings.TrimPrefix(line, "/"))
	if len(fields) == 0 {
		return "", nil, true // a bare "/" is a command with no name
	}
	return strings.ToLower(fields[0]), fields[1:], true
}

// dispatch interprets one line of input. It returns a message for the model, or
// an outcome telling the loop to keep prompting or to stop. Bad grammar is
// reported and treated as handled, never forwarded to the model.
func dispatch(c *cmdCtx, line string) (cmdOutcome, string) {
	line = strings.TrimSpace(line)
	// "//text" sends a literal leading slash to the model.
	if strings.HasPrefix(line, "//") {
		return cmdMessage, strings.TrimPrefix(line, "/")
	}

	name, args, isCmd := splitCommand(line)
	if !isCmd {
		return cmdMessage, line
	}
	if name == "" {
		c.notice("missing command name; try /help")
		return cmdHandled, ""
	}

	spec := lookupCommand(name)
	if spec == nil {
		c.notice("unknown command %q; try /help", "/"+name)
		return cmdHandled, ""
	}
	if len(args) < spec.minArgs || (spec.maxArgs >= 0 && len(args) > spec.maxArgs) {
		c.notice("usage: %s", spec.usage)
		return cmdHandled, ""
	}
	if spec.check != nil {
		if err := spec.check(args); err != nil {
			c.notice("%v", err)
			return cmdHandled, ""
		}
	}
	return spec.run(c, args)
}

// helpText is generated from the command table so it cannot go stale.
func helpText() string {
	var b strings.Builder
	b.WriteString("Commands:")
	for _, s := range commandSpecs {
		fmt.Fprintf(&b, "\n  %-16s %s", s.usage, s.summary)
	}
	b.WriteString("\n\nStart a line with // to send a literal leading slash to the model.")
	return b.String()
}

// ─── commands ────────────────────────────────────────────────────────────────

func runInfo(c *cmdCtx, _ []string) (cmdOutcome, string) {
	c.output(fmt.Sprintf("session: %s\nrole:    %s\nmodel:   %s\nturns:   %d",
		sessionName(c.session), c.cfg.DefaultRole, c.cfg.DefaultModel, len(c.rt.transcript)))
	return cmdHandled, ""
}

func runTools(c *cmdCtx, _ []string) (cmdOutcome, string) {
	if len(c.rp.Tools) == 0 {
		c.output("this role has no tools")
		return cmdHandled, ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "tools for role %s:", c.rp.Name)
	for _, t := range c.rp.Tools {
		fmt.Fprintf(&b, "\n  %s — %s", t.Name, t.Brief)
	}
	c.output(b.String())
	return cmdHandled, ""
}

func checkRoleName(args []string) error {
	if len(args) == 0 {
		return nil
	}
	for _, n := range prompts.RoleNames() {
		if n == args[0] {
			return nil
		}
	}
	return fmt.Errorf("unknown role %q; available: %s", args[0], strings.Join(prompts.RoleNames(), ", "))
}

func runRole(c *cmdCtx, args []string) (cmdOutcome, string) {
	if len(args) == 0 {
		c.output(fmt.Sprintf("role: %s\n%s", c.cfg.DefaultRole, c.rp.Description))
		return cmdHandled, ""
	}

	name := args[0]
	if name == c.cfg.DefaultRole {
		c.output(fmt.Sprintf("already running role %s", name))
		return cmdHandled, ""
	}

	// Reload the role's own custom prompt, not the previous role's.
	custom, err := config.LoadCustomPrompt(name)
	if err != nil {
		c.notice("%v", err)
		return cmdHandled, ""
	}
	rp, err := chat.LoadRolePrompt(name, custom)
	if err != nil {
		c.notice("%v", err)
		return cmdHandled, ""
	}
	if err := checkToolCoverage(rp); err != nil {
		c.notice("%v", err)
		return cmdHandled, ""
	}

	c.cfg.CustomPrompt = custom
	c.cfg.DefaultRole = name
	c.rp = rp
	c.output(fmt.Sprintf("role switched to %s (the system prompt changed, so this turn is not prefix-cached)", name))
	if rp.ContextSource != "" {
		c.output("project instructions: " + rp.ContextSource)
	}
	return cmdHandled, ""
}

func runNew(c *cmdCtx, args []string) (cmdOutcome, string) {
	name := ""
	if len(args) == 1 {
		name = args[0]
	}

	var rec core.Recorder
	if name != "" {
		if c.rt.Sessions == nil {
			c.notice("session storage is unavailable; cannot start a named session")
			return cmdHandled, ""
		}
		r, err := c.rt.Sessions.New(name)
		if err != nil {
			c.notice("%v", err)
			return cmdHandled, ""
		}
		rec = r
	} else if c.rt.Sessions != nil {
		// Switch back to an unpersisted conversation.
		r, err := c.rt.Sessions.New("")
		if err != nil {
			c.notice("%v", err)
			return cmdHandled, ""
		}
		rec = r
	}

	c.rt.Recorder = rec
	c.history = nil
	c.rt.transcript = nil
	c.session = name
	c.rt.Observer.Session(name)
	c.output(fmt.Sprintf("started a new conversation (%s)", sessionName(name)))
	return cmdHandled, ""
}

func runRename(c *cmdCtx, args []string) (cmdOutcome, string) {
	name := args[0]
	if name == c.session {
		c.output(fmt.Sprintf("this conversation is already named %q", name))
		return cmdHandled, ""
	}
	if c.rt.Sessions == nil {
		c.notice("session storage is unavailable; start with -s <name> to keep one")
		return cmdHandled, ""
	}

	rec, err := c.rt.Sessions.Persist(name, c.rt.transcript)
	if err != nil {
		c.notice("%v", err)
		return cmdHandled, ""
	}

	c.rt.Recorder = rec
	c.session = name
	c.rt.Observer.Session(name)
	c.output(fmt.Sprintf("session saved as %q", name))
	return cmdHandled, ""
}

// sessionName renders a session name for display; "" is unpersisted.
func sessionName(name string) string {
	if name == "" {
		return "<temp session>"
	}
	return name
}
