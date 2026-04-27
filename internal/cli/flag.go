package cli

import (
	"flag"
	"io"
	"strings"
)

type CliFlags struct {
	Debug  bool
	Multi  bool
	Action string
	Input  string
}

var Flags CliFlags

func GetFlags(args []string) error {
	fs := flag.NewFlagSet("pai", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.BoolVar(&Flags.Debug, "debug", false, "Enable debug mode")
	fs.BoolVar(&Flags.Debug, "d", false, "Enable debug mode (shorthand)")

	fs.BoolVar(&Flags.Multi, "inter", false, "Enable multi-turn chat")
	fs.BoolVar(&Flags.Multi, "i", false, "Enable multi-turn chat (shorthand)")

	fs.StringVar(&Flags.Action, "action", "", "pai's action")
	fs.StringVar(&Flags.Action, "a", "", "pai's action (shorthand)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	Flags.Input = strings.Join(fs.Args(), " ")
	return nil
}
