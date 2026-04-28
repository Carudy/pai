package cli

import (
	"flag"
	"io"
	"strings"

	"github.com/Carudy/pai/internal/config"
)

func GetFlags(args []string) error {
	fs := flag.NewFlagSet("pai", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.BoolVar(&config.AppFlags.Debug, "debug", false, "Enable debug mode")
	fs.BoolVar(&config.AppFlags.Debug, "d", false, "Enable debug mode (shorthand)")

	fs.BoolVar(&config.AppFlags.Inter, "inter", false, "Enable multi-turn chat")
	fs.BoolVar(&config.AppFlags.Inter, "i", false, "Enable multi-turn chat (shorthand)")

	fs.StringVar(&config.AppFlags.Agent, "agent", "", "pai's agent")
	fs.StringVar(&config.AppFlags.Agent, "a", "", "pai's agent (shorthand)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	config.AppFlags.Input = strings.Join(fs.Args(), " ")
	return nil
}
