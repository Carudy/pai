package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Carudy/pai/internal/cli"
)

func main() {
	// SIGINT is deliberately not trapped here: cli.Run handles it so Ctrl+C
	// cancels the in-flight step and returns to the prompt (ending the run only
	// when nothing is running). SIGTERM stays a hard shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Run(ctx, os.Stdout, os.Args[1:]))
}
