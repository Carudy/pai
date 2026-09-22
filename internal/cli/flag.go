package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// CliFlags holds all chat flag values parsed from os.Args.
type CliFlags struct {
	Version   bool
	Debug     bool
	Inter     bool
	Role      string
	Model     string
	Session   string
	Attach    string
	Continue  bool
	NoSession bool
	Input     string
}

// GetFlags parses CLI args and returns the resolved CliFlags. A bool return
// indicates that help was requested (caller should exit cleanly).
func GetFlags(args []string) (CliFlags, bool, error) {
	var flags CliFlags

	// Print help for bare "-h"/"--help".
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Print(generalHelp())
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

	fs.StringVar(&flags.Model, "model", "", "override the model as provider:model")
	fs.StringVar(&flags.Model, "m", "", "override the model (shorthand)")

	fs.StringVar(&flags.Session, "session", "", "use or create a named session")
	fs.StringVar(&flags.Session, "s", "", "use or create a named session (shorthand)")

	fs.StringVar(&flags.Attach, "attach", "", "resume an existing session by name")

	fs.BoolVar(&flags.Continue, "continue", false, "resume the most recent session for this directory")
	fs.BoolVar(&flags.Continue, "C", false, "resume the most recent session (shorthand)")

	fs.BoolVar(&flags.NoSession, "no-session", false, "do not persist this run")

	if err := fs.Parse(args); err != nil {
		return flags, false, err
	}

	flags.Input = strings.Join(fs.Args(), " ")
	return flags, false, nil
}
