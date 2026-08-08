package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Carudy/pai/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	code := cli.Run(ctx, os.Stdout, os.Args[1:])
	os.Exit(code)
}
