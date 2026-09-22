package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/provider"
	"github.com/Carudy/pai/internal/role"
	"github.com/Carudy/pai/internal/session"
	"github.com/Carudy/pai/internal/tui"
)

// Version is PAI's version string.
const Version = "v0.6.6"

// runChat parses chat flags, loads config, wires up the selected role, and runs
// it. It is both the `pai chat` handler and the default action for a bare `pai`.
func runChat(ctx context.Context, args []string, stdout io.Writer, log *tui.Logger) int {
	flags, helpRequested, err := GetFlags(args)
	if err != nil {
		log.Errorf("Error parsing flags: %v\n", err)
		return 1
	}
	if helpRequested {
		return 0
	}

	log.Debug = log.Debug || flags.Debug

	if flags.Version {
		fmt.Fprintf(stdout, "PAI version: %s\n", Version)
		return 0
	}

	cfg, err := config.LoadUserConfig()
	if err != nil {
		log.Errorf("Error loading config: %v\n", err)
		return 1
	}

	if flags.Role != "" {
		cfg.DefaultRole = flags.Role
	}
	if flags.Model != "" {
		cfg.SetModel(flags.Model)
	}

	// Config "interactive: true" auto-enables -i mode.
	interactive := flags.Inter || cfg.Interactive

	log.Debugf("📃 User flags: %#v\n", flags)
	log.Debugf("🔧 User config: %#v\n", cfg.Redacted())

	// Lazily load the custom prompt for the resolved role only.
	customPrompt, err := config.LoadCustomPrompt(cfg.DefaultRole)
	if err != nil {
		log.Errorf("Error loading custom prompt: %v\n", err)
		return 1
	}
	cfg.CustomPrompt = customPrompt

	log.Debugf("🔌 Connecting to %#v...\n", cfg.DefaultModel)
	providerCfg := cfg.ProvidersConfigs[cfg.Provider]
	client, err := provider.CreateClient(cfg.Provider, providerCfg.APIKey, cfg.Model, providerCfg.BaseURL)
	if err != nil {
		log.Errorf("Error creating LLM client: %v\n", err)
		return 1
	}

	userInput := strings.TrimSpace(flags.Input)
	if userInput == "" && !interactive {
		log.Errorf("Error: Please provide a user input\n")
		return 1
	}
	log.Debugf("💬 User input: %#v...\n", userInput)

	// Resolve the session (if any) and the turns to resume.
	store, sess, err := resolveSession(cfg, flags)
	if err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	var history []provider.Message
	if store != nil {
		defer store.Close()
		history = toMessages(sess.Turns)
		// Echo the tail of the transcript so the user has context, and print it
		// before the UI starts so it lands in normal scrollback.
		printRecap(stdout, sess, cfg.SessionRecapTurns)
		log.Debugf("Session: %s (%d resumed turns, %s backend)\n", sess.Meta.Name, len(history), session.Backend())
	}

	// The conversation's storage name; "" means this run is not persisted.
	sessionName := ""
	if store != nil {
		sessionName = sess.Meta.Name
	}

	// In-session storage commands (/rename, /new) go through a controller that can
	// open a store lazily, so a temporary run only touches disk if the user names
	// it. Closing it here releases a store it opened itself; one supplied by
	// resolveSession stays owned by the deferred Close above.
	cwd, _ := os.Getwd()
	controller := session.NewController(session.Open, store, sessionName, func() session.Meta {
		return session.Meta{Role: cfg.DefaultRole, Model: cfg.DefaultModel, Cwd: cwd}
	})
	defer controller.Close()

	// Ports and per-run state for this run live in a Runtime, kept out of
	// UserConfig (which holds configuration only).
	//
	// On a real terminal the whole run is handed to the inline UI: it keeps the
	// input bar available while PAI works (type-ahead queueing) and turns tool
	// confirmations into a modal prompt. Pipes keep the plain line adapters,
	// where whole-line reads and EOF semantics are the right behaviour.
	var app *tui.App
	if canUseTUI(stdout) {
		app = tui.NewApp(stdout, sessionName, interactive)
		log.SetWriter(app.Writer())
	}

	rt := &role.Runtime{
		Provider:    client,
		Observer:    tui.NewLineObserver(stdout),
		Prompter:    tui.NewPrompter(os.Stdin, stdout),
		Logger:      log,
		Interactive: interactive,
		Sessions:    controller,
		SessionName: sessionName,
	}
	if app != nil {
		rt.Observer = app.Observer()
		rt.Prompter = app.Prompter()
		app.SetInterrupt(rt.Interrupt)
	}
	if store != nil {
		rt.Recorder = session.NewRecorder(store, sess.Meta.Name)
	}

	// Ctrl+C cancels the in-flight step and drops back to the prompt; when no
	// step is running it ends the run. SIGTERM (handled in main) still shuts the
	// whole process down.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	go func() {
		for {
			select {
			case <-runCtx.Done():
				return
			case <-sigCh:
				if !rt.Interrupt() {
					cancelRun()
					return
				}
			}
		}
	}()

	log.Debugf("Entering role %s\n", cfg.DefaultRole)

	if app != nil {
		app.Start()
	}

	// Run the loop off the main goroutine so a cancelled context (SIGTERM) can
	// close the UI and thereby unblock a prompt the loop is parked on, instead of
	// leaving both sides waiting on each other.
	runErr := make(chan error, 1)
	go func() { runErr <- role.Run(runCtx, cfg, rt, history, userInput) }()
	if app != nil {
		go func() {
			<-runCtx.Done()
			app.Close()
		}()
	}

	err = <-runErr
	if app != nil {
		if uiErr := app.Err(); uiErr != nil {
			log.Errorf("Terminal UI error: %v\n", uiErr)
		}
		app.Close()
	}

	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(stdout, "\nInterrupted.")
			return 0
		}
		log.Errorf("Error in role %s: %v\n", cfg.DefaultRole, err)
		return 1
	}
	log.Debugf("Role %s exited successfully.\n", cfg.DefaultRole)
	return 0
}

// canUseTUI reports whether the inline UI can run: both stdin and stdout must be
// terminals, so raw mode and above-the-input rendering are viable.
func canUseTUI(stdout io.Writer) bool {
	return isCharDevice(os.Stdin) && isCharDevice(stdout)
}

// isCharDevice approximates "is a terminal" without pulling in a dependency.
func isCharDevice(w any) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
