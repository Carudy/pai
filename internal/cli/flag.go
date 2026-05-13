package cli

import (
	"flag"
	"io"
	"strings"

	"github.com/Carudy/pai/internal/hq"
)

func GetFlags(args []string) error {
	fs := flag.NewFlagSet("pai", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.BoolVar(&hq.Flags.Version, "version", false, "pai's version")
	fs.BoolVar(&hq.Flags.Version, "v", false, "pai's version (shorthand)")

	fs.BoolVar(&hq.Flags.Debug, "debug", false, "Enable debug mode")
	fs.BoolVar(&hq.Flags.Debug, "d", false, "Enable debug mode (shorthand)")

	fs.BoolVar(&hq.Flags.Inter, "inter", false, "Enable multi-turn chat")
	fs.BoolVar(&hq.Flags.Inter, "i", false, "Enable multi-turn chat (shorthand)")

	fs.StringVar(&hq.Flags.Agent, "agent", "", "pai's agent")
	fs.StringVar(&hq.Flags.Agent, "a", "", "pai's agent (shorthand)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	hq.Flags.Input = strings.Join(fs.Args(), " ")
	return nil
}
