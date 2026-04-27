package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"pai/internal/agent"
	"pai/internal/config"
	"pai/internal/llm"
	"pai/internal/tool"
	"pai/internal/ui"
)

func DebugLog(msg string) {
	if Flags.Debug {
		fmt.Print(ui.Styles["Debug"].Render(msg))
	}
}

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
	DebugLog(fmt.Sprintf("🔌 Connecting to %s...", cfg.DefaultModel))

	llm_client, err := llm.NewClient(cfg)
	if err != nil {
		fmt.Errorf("Error creating LLM client: %v\n", err)
		return 1
	}

	user_input := strings.TrimSpace(Flags.Input)
	if user_input == "" && !Flags.Multi {
		fmt.Errorf("Error: Please provide a user input")
		return 1
	}
	DebugLog(fmt.Sprintf("💬 User input: %s...", user_input))

	if Flags.Action == "" {
		Flags.Action = cfg.DefaultAgent
	}

	fmt.Printf("🤖 Processing...\n")
	switch Flags.Action {
	case "qa":
		DebugLog(fmt.Sprintf("Entering ask-ans agent; Inter: %v\n", Flags.Multi))
		if err := agent.AskQuestion(ctx, llm_client, cfg, user_input, Flags.Multi); err != nil {
			fmt.Errorf("Error in asking agent: %v\n", err)
			return 1
		}
		return 0

	case "cmd":
		result, err := agent.GenerateCommand(ctx, llm_client, user_input, cfg)
		if err != nil {
			fmt.Errorf("Error generating command: %v\n", err)
			return 1
		}

		fmt.Println(ui.Styles["Title"].Render("💡 Comment:"))
		fmt.Printf("\t%s\n", ui.Styles["Info"].Render(result.Comment))
		fmt.Println(ui.Styles["Title"].Render("💻 Command:"))
		fmt.Printf("\t%s\n", ui.Styles["Cmd"].Render(result.Cmd))

		exe_res, err := ui.GetUserSelected("Execute the command ?", []string{"Yes", "No"})
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
			fmt.Println(ui.Styles["Success"].Render("Executed successfully."))
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
