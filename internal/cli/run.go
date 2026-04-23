package cli

import (
	"context"
	"fmt"
	"io"

	"pai/internal/agent"
	"pai/internal/config"
	"pai/internal/llm"
	"pai/internal/tool"
)

func Run(ctx context.Context, stdin io.Reader, stdout io.Writer, args []string) int {
	GetFlags(args)

	DebugLog(fmt.Sprintf("📃 User Flags: %v\n", Flags))
	DebugLog("🔧 Loading configuration...\n")

	cfg, err := config.LoadUserConfig()
	if err != nil {
		fmt.Errorf("Error loading config: %v\n", err)
		return 1
	}

	DebugLog(fmt.Sprintf("🔧 User config: %+v", cfg))
	DebugLog(fmt.Sprintf("🔌 Connecting to %s...", cfg.Provider))

	provider, err := llm.NewClient(cfg)
	if err != nil {
		fmt.Errorf("Error creating LLM client: %v\n", err)
		return 1
	}

	userInput := Flags.Input
	if userInput == "" {
		fmt.Errorf("Error: Please provide a user input")
		return 1
	}

	fmt.Printf("🤖 Processing...\n")

	switch Flags.Action {
	case "ask":
		result, err := agent.AskQuestion(ctx, provider, userInput, cfg)
		if err != nil {
			fmt.Errorf("Error asking question: %v\n", err)
			return 1
		}

		fmt.Printf("💡 Answer:\n")
		fmt.Printf(Styles.Cmd.Render(result))
		return 0

	case "cmd":
		result, err := agent.GenerateCommand(ctx, provider, userInput, cfg)
		if err != nil {
			fmt.Errorf("Error generating command: %v\n", err)
			return 1
		}

		fmt.Println(Styles.Title.Render("💡 Comment:"))
		fmt.Printf("\t%s\n", Styles.Info.Render(result.Comment))
		fmt.Println(Styles.Title.Render("💻 Command:"))
		fmt.Printf("\t%s\n", Styles.Cmd.Render(result.Cmd))

		exe_res, err := GetUserSelected("Execute the command ?", []string{"Yes", "No"})
		if err != nil {
			fmt.Errorf("Error while interacting with user", err)
			return 1
		}

		if exe_res == "No" {
			fmt.Printf("Execution cancelled.")
			return 0
		} else if exe_res == "Yes" {
			output, err := tool.ExecuteCommand(stdout, result.Cmd)
			if err != nil {
				fmt.Errorf("Execution failed: %v\nOutput: %s\n", err, string(output))
				return 1
			}
			fmt.Println(Styles.Success.Render("Executed successfully."))
			fmt.Printf(fmt.Sprintf("%v\n", output))
		} else {
			fmt.Printf("Aborted.")
			return 0
		}

		return 0

	default:
		fmt.Errorf("Unsupported PAI action: %s", Flags.Action)
		return 1
	}
}
