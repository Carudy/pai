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

var AgentMap = map[string]func(ctx context.Context, cfg *config.UserConfig, user_input string) error{
	"qa":     agent.QA,
	"cmd":    agent.GenCMD,
	"devops": agent.DevOps,
}

func Run(ctx context.Context, stdin io.Reader, stdout io.Writer, args []string) int {
	GetFlags(args)

	if config.AppFlags.Version {
		fmt.Fprintf(stdout, "Pai verion: %s\n", config.PAI_VERSION)
		return 0
	}

	cfg, err := config.LoadUserConfig()
	if err != nil {
		config.ErrorLog(stdout, "Error loading config: %v\n", err)
		return 1
	}

	cfg.Flags = &config.AppFlags

	config.DebugLog(stdout, "📃 User Flags: %v\n", config.AppFlags)
	config.DebugLog(stdout, "🔧 User config: %v\n", cfg)

	if config.AppFlags.Agent != "" {
		cfg.DefaultAgent = config.AppFlags.Agent
	}

	config.DebugLog(stdout, "🔌 Connecting to %s...\n", cfg.DefaultModel)
	llm_client, err := llm.CreateClient(cfg.Provider, cfg.APIKeys[cfg.Provider], cfg.Model, cfg.Reasoning)
	if err != nil {
		config.ErrorLog(stdout, "Error creating LLM client: %v\n", err)
		return 1
	}
	cfg.Clients[cfg.DefaultAgent] = llm_client

	user_input := strings.TrimSpace(config.AppFlags.Input)
	if user_input == "" && !config.AppFlags.Inter {
		config.ErrorLog(stdout, "Error: Please provide a user input\n")
		return 1
	}
	config.DebugLog(stdout, "💬 User input: %s...\n", user_input)

	if agent_func, ok := AgentMap[cfg.DefaultAgent]; ok {
		config.DebugLog(stdout, "Entering %s agent\n", cfg.DefaultAgent)
		if err := agent_func(ctx, cfg, user_input); err != nil {
			config.ErrorLog(stdout, "Error in %s agent: %v\n", cfg.DefaultAgent, err)
			return 1
		}
		config.DebugLog(stdout, "Agent %s exit successfully.\n", cfg.DefaultAgent)
		return 0
	} else {
		config.ErrorLog(stdout, "Error: Unsupported PAI agent: \"%s\"\n", cfg.DefaultAgent)
		return 1
	}
}
