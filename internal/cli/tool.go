package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/tui"
)

// toolHelp is the detailed help for `pai tool`.
func toolHelp() string {
	return `SUBCOMMANDS
  list, ls                 List built-in and user tools with their layer/source
  show, cat <name>         Show a tool definition, parameters, and executable spec
	  check <name> --json J    Validate a payload and render the argv or request

Custom tools are TOML files in $XDG_CONFIG_HOME/pai/tools/. A role must still
list a tool in its "tools" array (or role-extra.toml) before the model can call
it. A trusted workspace (see [tool] trusted_projects) also contributes
.pai/tools/*.toml; those project tools are appended to every role there and
their grants are session-only.`
}

// runTool handles `pai tool <list|show|check>`.
func runTool(_ context.Context, args []string, stdout io.Writer, log *tui.Logger) int {
	cfg, err := config.LoadUserConfig()
	if err != nil {
		log.Errorf("Error loading config: %v\n", err)
		return 1
	}
	cwd, _ := os.Getwd()
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list", "ls":
		return toolListCmd(stdout, log, cwd, cfg.TrustedProjects)
	case "show", "cat":
		if len(args) < 2 {
			log.Errorf("Usage: pai tool show <name>\n")
			return 1
		}
		return toolShow(args[1], stdout, log, cwd, cfg.TrustedProjects)
	case "check":
		return toolCheck(args[1:], stdout, log, cwd, cfg.TrustedProjects)
	default:
		log.Errorf("Unknown tool command %q; try: list | show | check\n", args[0])
		return 1
	}
}

func toolListCmd(stdout io.Writer, log *tui.Logger, cwd string, trustedProjects []string) int {
	projectNames, err := prompts.ProjectToolNames(cwd, trustedProjects)
	if err != nil {
		log.Errorf("Error: %v\n", err)
		projectNames = nil
	}
	names := append(append([]string{}, prompts.ToolNames()...), prompts.UserToolNames()...)
	names = append(names, projectNames...)
	sort.Strings(names)
	roles := toolRoles()
	seen := make(map[string]bool, len(names))
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tLAYER\tROLES\tSOURCE\tBRIEF")
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		t, err := prompts.ResolveToolIn(name, cwd, trustedProjects)
		roleText := strings.Join(roles[name], ",")
		if err != nil {
			fmt.Fprintf(tw, "%s\t-\t-\t%s\t(%v)\t\n", name, roleText, err)
			continue
		}
		if roleText == "" && t.Layer == prompts.LayerProject {
			roleText = "project"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.Name, t.Kind, t.Layer, roleText, t.Source, t.Brief)
	}
	return flush(tw)
}

// toolRoles maps a tool name to the roles that list it, including role-extra
// additions, so `pai tool ls` answers "is this callable, and by whom?".
func toolRoles() map[string][]string {
	out := make(map[string][]string)
	for _, role := range prompts.RoleNames() {
		def, err := prompts.LoadRole(role)
		if err != nil {
			continue
		}
		_, extra, err := config.LoadRoleExtraEntry(role)
		if err != nil {
			extra = nil
		}
		seen := make(map[string]bool)
		for _, name := range append(append([]string{}, def.Tools...), extra...) {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out[name] = append(out[name], role)
		}
	}
	for name := range out {
		sort.Strings(out[name])
	}
	return out
}

func toolShow(name string, stdout io.Writer, log *tui.Logger, cwd string, trustedProjects []string) int {
	t, err := prompts.ResolveToolIn(name, cwd, trustedProjects)
	if err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "name:        %s\n", t.Name)
	fmt.Fprintf(stdout, "kind:        %s\n", t.Kind)
	fmt.Fprintf(stdout, "layer:       %s\n", t.Layer)
	fmt.Fprintf(stdout, "source:      %s\n", t.Source)
	fmt.Fprintf(stdout, "hash:        %s\n", t.DefinitionHash)
	fmt.Fprintf(stdout, "brief:       %s\n", t.Brief)
	if s := strings.TrimSpace(t.Schema); s != "" {
		fmt.Fprintf(stdout, "payload:     %s\n", s)
	}
	if t.OutputLimit > 0 {
		fmt.Fprintf(stdout, "output_limit: %d bytes\n", t.OutputLimit)
	}
	if len(t.Params) > 0 {
		fmt.Fprintln(stdout, "parameters:")
		for _, p := range t.Params {
			fmt.Fprintf(stdout, "  %s: %s", p.Name, p.Type)
			if p.Required {
				fmt.Fprint(stdout, ", required")
			}
			if p.Default != nil {
				fmt.Fprintf(stdout, ", default=%v", p.Default)
			}
			if p.Min != nil || p.Max != nil {
				fmt.Fprintf(stdout, ", range=%v..%v", valueOr(p.Min, "-inf"), valueOr(p.Max, "+inf"))
			}
			if p.MaxLength > 0 {
				fmt.Fprintf(stdout, ", max_length=%d", p.MaxLength)
			}
			if p.Pattern != "" {
				fmt.Fprintf(stdout, ", pattern=%s", p.Pattern)
			}
			fmt.Fprintln(stdout)
		}
	}
	switch {
	case t.CLI != nil:
		fmt.Fprintf(stdout, "cli.command: %s\n", t.CLI.Command)
		if len(t.CLI.Args) > 0 {
			fmt.Fprintf(stdout, "cli.args:    %s\n", strings.Join(t.CLI.Args, " "))
		}
		if t.CLI.Dir != "" {
			fmt.Fprintf(stdout, "cli.dir:     %s\n", t.CLI.Dir)
		}
		if t.CLI.TimeoutSeconds > 0 {
			fmt.Fprintf(stdout, "cli.timeout: %ds\n", t.CLI.TimeoutSeconds)
		}
	case t.HTTP != nil:
		fmt.Fprintf(stdout, "http:        %s %s\n", t.HTTP.Method, t.HTTP.URL)
	}
	if d := strings.TrimSpace(t.Description); d != "" {
		fmt.Fprintf(stdout, "\n%s\n", d)
	}
	return 0
}

func toolCheck(args []string, stdout io.Writer, log *tui.Logger, cwd string, trustedProjects []string) int {
	if len(args) == 0 {
		log.Errorf("Usage: pai tool check <name> --json '<payload>'\n")
		return 1
	}
	name := args[0]
	var payload string
	for i := 1; i < len(args); i++ {
		if args[i] == "--json" && i+1 < len(args) {
			payload = args[i+1]
			i++
			continue
		}
		log.Errorf("Unknown argument %q; usage: pai tool check <name> --json '<payload>'\n", args[i])
		return 1
	}
	if payload == "" {
		log.Errorf("Usage: pai tool check <name> --json '<payload>'\n")
		return 1
	}
	t, err := prompts.ResolveToolIn(name, cwd, trustedProjects)
	if err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	if t.Kind != prompts.ToolCLI && t.Kind != prompts.ToolHTTP {
		log.Errorf("Error: tool %q is %s; check supports cli and http tools\n", name, t.Kind)
		return 1
	}
	values, err := t.ValidatePayload(json.RawMessage(payload))
	if err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "tool:   %s\n", t.Name)
	fmt.Fprintf(stdout, "source: %s\n", t.Source)
	if t.Kind == prompts.ToolHTTP {
		target, err := t.RenderURL(values)
		if err != nil {
			log.Errorf("Error: %v\n", err)
			return 1
		}
		body, err := t.RenderBody(values)
		if err != nil {
			log.Errorf("Error: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "request: %s %s\n", strings.ToUpper(t.HTTP.Method), target)
		names := make([]string, 0, len(t.HTTP.Headers))
		for key := range t.HTTP.Headers {
			names = append(names, key)
		}
		sort.Strings(names)
		if t.HTTP.Auth != "" {
			names = append(names, "Authorization (from environment)")
		}
		if len(names) > 0 {
			fmt.Fprintf(stdout, "headers: %s\n", strings.Join(names, ", "))
		}
		if len(body) > 0 {
			fmt.Fprintf(stdout, "body:    %s\n", body)
		}
		fmt.Fprintln(stdout, "executed: no (dry run)")
		return 0
	}
	rendered, err := t.RenderArgs(values)
	if err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	argv := append([]string{t.CLI.Command}, rendered...)
	fmt.Fprintf(stdout, "argv:   %s\n", prompts.FormatArgv(argv))
	fmt.Fprintln(stdout, "executed: no (dry run)")
	return 0
}

func valueOr[T any](p *T, fallback string) string {
	if p == nil {
		return fallback
	}
	return fmt.Sprint(*p)
}
