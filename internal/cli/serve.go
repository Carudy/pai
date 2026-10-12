package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/Carudy/pai/internal/runner"
	"github.com/Carudy/pai/internal/session"
	"github.com/Carudy/pai/internal/tui"
	"github.com/Carudy/pai/internal/web"
)

func serveHelp() string {
	return `FLAGS
  --ip, -ip <IP>                 Bind IP (default 127.0.0.1; no hostnames)
  --port, -p <port>              Bind port (default 9384)
  --max-active-sessions <n>      Maximum live workers (default 2)
  --activity-log                Log tool calls/lifecycle to stdout (default false)
  --token-file <path>            Token file; overrides PAI_SERVE_TOKEN
  --public-origin <origin>       Browser origin, e.g. https://pai.example.com
  --help, -h                    Show help

Non-loopback binds and --public-origin require a token of at least 32 characters.
For reverse proxies, preserving Host is recommended. Set --public-origin when
terminating TLS or changing Host; forwarded headers are never trusted.
Activity logs omit reasoning and tool output; commands may contain sensitive data.
Remote access requires TLS or a trusted TLS terminator. Clients can run tools
with this process's permissions. Sessions use their saved workspace; new sessions default to the starting cwd.`
}

type serveFlags struct {
	ip              string
	port, maxActive int
	tokenFile       string
	publicOrigin    string
	help            bool
	activityLog     bool
}

func parseServeFlags(args []string) (serveFlags, error) {
	var f serveFlags
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&f.ip, "ip", "127.0.0.1", "bind IP")
	fs.IntVar(&f.port, "port", 9384, "bind port")
	fs.IntVar(&f.port, "p", 9384, "bind port")
	fs.IntVar(&f.maxActive, "max-active-sessions", 2, "maximum live workers")
	fs.StringVar(&f.tokenFile, "token-file", "", "token file")
	fs.StringVar(&f.publicOrigin, "public-origin", "", "canonical browser origin")
	fs.BoolVar(&f.activityLog, "activity-log", false, "log activity to stdout")
	fs.BoolVar(&f.help, "help", false, "help")
	fs.BoolVar(&f.help, "h", false, "help")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if f.help {
		return f, nil
	}
	if fs.NArg() != 0 {
		return f, errors.New("serve accepts no positional arguments")
	}
	if net.ParseIP(f.ip) == nil {
		return f, errors.New("bind address must be an IP literal")
	}
	if f.port < 1 || f.port > 65535 {
		return f, errors.New("port must be between 1 and 65535")
	}
	if f.maxActive < 1 {
		return f, errors.New("max-active-sessions must be positive")
	}
	publicOriginSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "public-origin" {
			publicOriginSet = true
		}
	})
	if publicOriginSet {
		origin, err := canonicalServeOrigin(f.publicOrigin)
		if err != nil {
			return f, err
		}
		f.publicOrigin = origin
	}
	return f, nil
}

func runServe(ctx context.Context, args []string, stdout io.Writer, log *tui.Logger) int {
	f, err := parseServeFlags(args)
	if err != nil {
		log.Errorf("serve: %v\n", err)
		return 1
	}
	if f.help {
		printCommandHelp(stdout, findCommand("serve"))
		return 0
	}
	token, err := serveToken(f)
	if err != nil {
		log.Errorf("serve: %v\n", err)
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		log.Errorf("serve cwd: %v\n", err)
		return 1
	}
	store, err := session.Open()
	if err != nil {
		log.Errorf("open session store: %v\n", err)
		return 1
	}
	defer store.Close()
	listener, err := net.Listen("tcp", net.JoinHostPort(f.ip, fmt.Sprint(f.port)))
	if err != nil {
		log.Errorf("listen: %v\n", err)
		return 1
	}
	defer listener.Close()
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	manager := runner.New(runCtx, &serveBackend{store: store, cwd: cwd}, f.maxActive)
	defer manager.Close()
	handler := web.New(manager, web.Options{Token: token, PublicOrigin: f.publicOrigin, ServerCwd: cwd})
	// Origin checks alone do not prevent DNS rebinding on unauthenticated loopback.
	host := listener.Addr().String()
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" && r.Host != host {
				http.Error(w, "invalid host", http.StatusForbidden)
				return
			}
			handler.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	result := make(chan error, 1)
	fmt.Fprintf(stdout, "Serving http://%s (workspace %s)\n", listener.Addr(), cwd)
	if f.activityLog {
		stopLog, logErr := startServeActivityLog(manager, stdout)
		if logErr != nil {
			log.Errorf("activity log: %v\n", logErr)
			return 1
		}
		defer stopLog()
	}
	go func() { result <- server.Serve(listener) }()
	select {
	case err = <-result:
	case <-runCtx.Done():
	}
	// Closing the manager first unblocks pending questions and SSE subscribers.
	manager.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		server.Close()
		log.Errorf("shutdown server: %v\n", shutdownErr)
		return 1
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Errorf("serve: %v\n", err)
		return 1
	}
	return 0
}
