package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/provider"
	"github.com/Carudy/pai/internal/role"
	"github.com/Carudy/pai/internal/session"
	"github.com/Carudy/pai/internal/tui"
)

// Version is PAI's version string.
const Version = "v0.5.0"

// Run is the main entry point for the PAI CLI. It parses flags, loads config,
// wires up the selected role, and executes it. Returns an exit code.
func Run(ctx context.Context, stdout io.Writer, args []string) int {
	log := tui.NewLogger(stdout, false)

	// `pai session …` is a management subcommand, not a chat.
	if len(args) > 0 && args[0] == "session" {
		return runSession(args[1:], stdout, log)
	}

	flags, helpRequested, err := GetFlags(args)
	if err != nil {
		log.Errorf("Error parsing flags: %v\n", err)
		return 1
	}
	if helpRequested {
		return 0
	}

	log.Debug = flags.Debug

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

	// Config "interactive: true" auto-enables -i mode.
	interactive := flags.Inter || cfg.Interactive

	log.Debugf("📃 User flags: %#v\n", flags)
	log.Debugf("🔧 User config: %#v\n", cfg)

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
	store, sessName, history, err := resolveSession(cfg, flags)
	if err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	if store != nil {
		defer store.Close()
		log.Debugf("Session: %s (%d resumed turns, %s backend)\n", sessName, len(history), session.Backend())
	}

	// Ports and per-run state for this run live in a Runtime, kept out of
	// UserConfig (which holds configuration only).
	rt := &role.Runtime{
		Provider:    client,
		Observer:    tui.NewLineObserver(stdout),
		Prompter:    tui.NewPrompter(),
		Logger:      log,
		Interactive: interactive,
	}
	if store != nil {
		rt.Recorder = session.NewRecorder(store, sessName)
	}

	log.Debugf("Entering role %s\n", cfg.DefaultRole)
	if err := role.Run(ctx, cfg, rt, history, userInput); err != nil {
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
