package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Carudy/pai/internal/agent"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/llm"
)

var AgentMap = map[string]func(ctx context.Context, cfg *config.UserConfig, userInput string) error{
	"qa":     agent.QA,
	"cmd":    agent.GenCMD,
	"devops": agent.DevOps,
}

func Run(ctx context.Context, stdout io.Writer, args []string) int {
	if err := GetFlags(args); err != nil {
		config.ErrorLog(stdout, "Error parsing flags: %v\n", err)
		return 1
	}

	if config.AppFlags.Version {
		fmt.Fprintf(stdout, "Pai version: %s\n", config.PAI_VERSION)
		return 0
	}

	cfg, err := config.LoadUserConfig()
	if err != nil {
		config.ErrorLog(stdout, "Error loading config: %v\n", err)
		return 1
	}

	flags := config.AppFlags
	cfg.Flags = &flags

	// Enable low-level LLM debug logging when --debug is set.
	llm.Debug = config.AppFlags.Debug

	config.DebugLog(stdout, "📃 User Flags: %s\n", config.AppFlags)
	config.DebugLog(stdout, "🔧 User config: %s\n", cfg)

	if config.AppFlags.Agent != "" {
		cfg.DefaultAgent = config.AppFlags.Agent
	}

	// Validate agent early, before loading prompt or creating LLM client.
	agentFunc, ok := AgentMap[cfg.DefaultAgent]
	if !ok {
		config.ErrorLog(stdout, "Error: Unsupported PAI agent: \"%s\"\n", cfg.DefaultAgent)
		return 1
	}

	// Lazily load the custom prompt for the resolved agent only.
	customPrompt, err := config.LoadCustomPrompt(cfg.DefaultAgent)
	if err != nil {
		config.ErrorLog(stdout, "Error loading custom prompt: %v\n", err)
		return 1
	}
	cfg.CustomPrompt = customPrompt

	config.DebugLog(stdout, "🔌 Connecting to %s...\n", cfg.DefaultModel)
	providerCfg := cfg.Providers[cfg.Provider]
	llmClient, err := llm.CreateClient(cfg.Provider, providerCfg.APIKey, cfg.Model, providerCfg.BaseURL)
	if err != nil {
		config.ErrorLog(stdout, "Error creating LLM client: %v\n", err)
		return 1
	}
	cfg.Clients[cfg.DefaultAgent] = llmClient

	userInput := strings.TrimSpace(config.AppFlags.Input)
	if userInput == "" && !config.AppFlags.Inter {
		config.ErrorLog(stdout, "Error: Please provide a user input\n")
		return 1
	}
	config.DebugLog(stdout, "💬 User input: %s...\n", userInput)

	config.DebugLog(stdout, "Entering %s agent\n", cfg.DefaultAgent)
	if err := agentFunc(ctx, cfg, userInput); err != nil {
		config.ErrorLog(stdout, "Error in %s agent: %v\n", cfg.DefaultAgent, err)
		return 1
	}
	config.DebugLog(stdout, "Agent %s exit successfully.\n", cfg.DefaultAgent)
	return 0
}
