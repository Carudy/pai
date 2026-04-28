package main

import (
	"context"
	"os"

	"github.com/Carudy/pai/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Stdin, os.Stdout, os.Args[1:]))
}
