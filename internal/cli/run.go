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

var AgentMap = map[string]func(ctx context.Context, cfg *config.UserConfig, userInput string) error{
	"qa":     agent.QA,
	"cmd":    agent.GenCMD,
	"devops": agent.DevOps,
}

func Run(ctx context.Context, stdout io.Writer, args []string) int {
	if err := GetFlags(args); err != nil {
		hq.ErrorLog(stdout, "Error parsing flags: %v\n", err)
		return 1
	}

	if hq.Flags.Version {
		fmt.Fprintf(stdout, "PAI version: %s\n", hq.PAI_VERSION)
		return 0
	}

	cfg, err := config.LoadUserConfig()
	if err != nil {
		hq.ErrorLog(stdout, "Error loading config: %v\n", err)
		return 1
	}

	flags := hq.Flags
	cfg.Flags = &flags

	hq.DebugLog(stdout, "📃 User Flags: %#v\n", hq.Flags)
	hq.DebugLog(stdout, "🔧 User config: %#v\n", cfg)

	if hq.Flags.Agent != "" {
		cfg.DefaultAgent = hq.Flags.Agent
	}

	// Validate agent early, before loading prompt or creating LLM client.
	agentFunc, ok := AgentMap[cfg.DefaultAgent]
	if !ok {
		hq.ErrorLog(stdout, "Error: Unsupported PAI agent: \"%s\"\n", cfg.DefaultAgent)
		return 1
	}

	// Lazily load the custom prompt for the resolved agent only.
	customPrompt, err := config.LoadCustomPrompt(cfg.DefaultAgent)
	if err != nil {
		hq.ErrorLog(stdout, "Error loading custom prompt: %v\n", err)
		return 1
	}
	cfg.CustomPrompt = customPrompt

	hq.DebugLog(stdout, "🔌 Connecting to %#v...\n", cfg.DefaultModel)
	providerCfg := cfg.ProvidersConfigs[cfg.Provider]
	llmClient, err := llm.CreateClient(cfg.Provider, providerCfg.APIKey, cfg.Model, providerCfg.BaseURL)
	if err != nil {
		hq.ErrorLog(stdout, "Error creating LLM client: %v\n", err)
		return 1
	}
	cfg.Clients[cfg.DefaultAgent] = llmClient

	userInput := strings.TrimSpace(hq.Flags.Input)
	if userInput == "" && !hq.Flags.Inter {
		hq.ErrorLog(stdout, "Error: Please provide a user input\n")
		return 1
	}
	hq.DebugLog(stdout, "💬 User input: %#v...\n", userInput)

	hq.DebugLog(stdout, "Entering %s agent\n", cfg.DefaultAgent)
	if err := agentFunc(ctx, cfg, userInput); err != nil {
		hq.ErrorLog(stdout, "Error in %s agent: %v\n", cfg.DefaultAgent, err)
		return 1
	}
	hq.DebugLog(stdout, "Agent %s exit successfully.\n", cfg.DefaultAgent)
	return 0
}
