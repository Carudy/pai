package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/Carudy/pai/internal/conn"
	"github.com/Carudy/pai/internal/tui"
)

func connHelp() string {
	return `FLAGS
  --url <url>         pai serve URL (default $PAI_SERVE_URL or ` + conn.DefaultURL + `)
  --token <token>     access token (default $PAI_SERVE_TOKEN)
  --origin <url>      origin sent on mutations (default: the --url origin)
  --session, -s <n>   session to attach to (or pass it as the argument)

Attaches to a session served by "pai serve" and streams its events to this
terminal, rendered like a local run. Type an instruction to send it. When a
confirmation appears, answer y (once), s (session), a (always) or n (deny).
/quit detaches; Ctrl+C cancels the remote step and leaves.

This client never opens the session store, so it works while "pai serve" holds
the store's exclusive lock.`
}

// lockedWriter serializes the SSE renderer and the local input loop, which write
// from different goroutines.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func runConn(ctx context.Context, args []string, stdout io.Writer, log *tui.Logger) int {
	fs := flag.NewFlagSet("conn", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		rawURL  = fs.String("url", "", "pai serve URL")
		token   = fs.String("token", "", "access token")
		origin  = fs.String("origin", "", "origin sent on mutations")
		session = fs.String("session", "", "session to attach to")
	)
	fs.StringVar(session, "s", "", "session to attach to (shorthand)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stdout, "conn: %v\n\n%s\n", err, connHelp())
		return 2
	}
	if *session == "" && fs.NArg() > 0 {
		*session = fs.Arg(0)
	}
	if *session == "" {
		fmt.Fprintf(stdout, "conn: a session name is required\n\n%s\n", connHelp())
		return 2
	}
	if *token == "" {
		*token = os.Getenv("PAI_SERVE_TOKEN")
	}
	if *rawURL == "" {
		*rawURL = os.Getenv("PAI_SERVE_URL")
	}
	client := conn.New(*rawURL, *token)
	if *origin != "" {
		client.Origin = *origin
	}

	pingCtx, stopPing := context.WithTimeout(ctx, 5*time.Second)
	err := client.Ping(pingCtx)
	stopPing()
	if err != nil {
		log.Errorf("conn: %v\n", err)
		return 1
	}

	out := &lockedWriter{w: stdout}
	observer := tui.NewLineObserver(out)

	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	// Ctrl+C stops the remote step (so the server is not left running) and leaves.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
			stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = client.Cancel(stopCtx, *session)
			cancel()
			stop()
			os.Exit(0)
		case <-runCtx.Done():
		}
	}()

	if err := conn.Run(runCtx, conn.Options{Client: client, Session: *session, In: os.Stdin, Out: out, Observer: observer}); err != nil && !errors.Is(err, context.Canceled) {
		log.Errorf("conn: %v\n", err)
		return 1
	}
	return 0
}
