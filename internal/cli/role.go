package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/BurntSushi/toml"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/tui"
)

// roleDef is the on-disk shape of a role definition (roles/<name>.toml).
type roleDef struct {
	Name        string   `toml:"name"`
	Description string   `toml:"description"`
	Intro       string   `toml:"intro"`
	Tools       []string `toml:"tools"`
}

// roleHelp is the detailed help for `pai role`.
func roleHelp() string {
	return `SUBCOMMANDS
  list, ls         List roles with their tools
  show, cat <name> Show a role's intro and tools

Roles are data: built-in definitions live in the binary, and users can add or
override their own at $XDG_CONFIG_HOME/pai/roles/<name>.toml.`
}

// runRole handles `pai role <list|show>`.
func runRole(_ context.Context, args []string, stdout io.Writer, log *tui.Logger) int {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list", "ls":
		return roleListCmd(stdout, log)
	case "show", "cat":
		if len(args) < 2 {
			log.Errorf("Usage: pai role show <name>\n")
			return 1
		}
		return roleShow(args[1], stdout, log)
	default:
		log.Errorf("Unknown role command %q; try: list | show\n", args[0])
		return 1
	}
}

func roleListCmd(stdout io.Writer, log *tui.Logger) int {
	names := prompts.RoleNames()
	if len(names) == 0 {
		fmt.Fprintln(stdout, "No roles found.")
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTOOLS\tDESCRIPTION")
	for _, n := range names {
		def, _, err := readRoleDef(n)
		if err != nil {
			fmt.Fprintf(tw, "%s\t%s\t(%v)\n", n, "-", err)
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", n, strings.Join(def.Tools, ", "), def.Description)
	}
	return flush(tw)
}

func roleShow(name string, stdout io.Writer, log *tui.Logger) int {
	def, source, err := readRoleDef(name)
	if err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	if def.Name == "" {
		def.Name = name
	}
	fmt.Fprintf(stdout, "name:        %s\n", def.Name)
	fmt.Fprintf(stdout, "source:      %s\n", source)
	fmt.Fprintf(stdout, "description: %s\n", def.Description)
	fmt.Fprintf(stdout, "tools:       %s\n", strings.Join(def.Tools, ", "))
	fmt.Fprintf(stdout, "\n%s\n", strings.TrimSpace(def.Intro))
	return 0
}

func readRoleDef(name string) (roleDef, string, error) {
	data, source, err := prompts.ReadRole(name)
	if err != nil {
		return roleDef{}, "", err
	}
	var def roleDef
	if err := toml.Unmarshal(data, &def); err != nil {
		return roleDef{}, source, fmt.Errorf("parse %s: %w", source, err)
	}
	return def, source, nil
}
