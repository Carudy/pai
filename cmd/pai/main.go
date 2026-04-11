package main

import (
	"context"
	"os"

	"pai/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Stdin, os.Stdout, os.Args[1:]))
}
