package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Carudy/pai/internal/agent"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/hq"
	"github.com/Carudy/pai/internal/llm"
)

// Run is the main entry point for the PAI CLI. It parses flags, loads
// config, wires up the selected agent, and executes it. Returns an exit code.
func Run(ctx context.Context, stdout io.Writer, args []string) int {
	log := hq.NewLogger(stdout, false)

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
		fmt.Fprintf(stdout, "PAI version: %s\n", hq.PAI_VERSION)
		return 0
	}

	cfg, err := config.LoadUserConfig()
	if err != nil {
		log.Errorf("Error loading config: %v\n", err)
		return 1
	}

	cfg.Flags = &flags
	cfg.Logger = log

	// Config "interactive: true" auto-enables -i mode.
	if cfg.Interactive {
		flags.Inter = true
	}

	log.Debugf("📃 User Flags: %#v\n", flags)
	log.Debugf("🔧 User config: %#v\n", cfg)

	if flags.Agent != "" {
		cfg.DefaultAgent = flags.Agent
	}

	// Look up the agent via the shared registry.
	selectedAgent := agent.Get(cfg.DefaultAgent)
	if selectedAgent == nil {
		log.Errorf("Error: Unsupported PAI agent: %q\n", cfg.DefaultAgent)
		return 1
	}

	// Lazily load the custom prompt for the resolved agent only.
	customPrompt, err := config.LoadCustomPrompt(cfg.DefaultAgent)
	if err != nil {
		log.Errorf("Error loading custom prompt: %v\n", err)
		return 1
	}
	cfg.CustomPrompt = customPrompt

	log.Debugf("🔌 Connecting to %#v...\n", cfg.DefaultModel)
	providerCfg := cfg.ProvidersConfigs[cfg.Provider]
	llmClient, err := llm.CreateClient(cfg.Provider, providerCfg.APIKey, cfg.Model, providerCfg.BaseURL)
	if err != nil {
		log.Errorf("Error creating LLM client: %v\n", err)
		return 1
	}
	cfg.Clients[cfg.DefaultAgent] = llmClient

	userInput := strings.TrimSpace(flags.Input)
	if userInput == "" && !flags.Inter {
		log.Errorf("Error: Please provide a user input\n")
		return 1
	}
	log.Debugf("💬 User input: %#v...\n", userInput)

	log.Debugf("Entering %s agent\n", cfg.DefaultAgent)
	if err := selectedAgent.Run(ctx, cfg, userInput); err != nil {
		log.Errorf("Error in %s agent: %v\n", cfg.DefaultAgent, err)
		return 1
	}
	log.Debugf("Agent %s exit successfully.\n", cfg.DefaultAgent)
	return 0
}
