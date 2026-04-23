package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"pai/internal/agent"
	"pai/internal/config"
	"pai/internal/llm"
)

func Run(ctx context.Context, stdin io.Reader, stdout io.Writer, args []string) int {
	GetFlags(args)

	DebugLog(fmt.Sprintf("📃 User Flags: %v\n", Flags))
	DebugLog("🔧 Loading configuration...\n")

	cfg, err := config.LoadUserConfig()
	if err != nil {
		ErrorLog(fmt.Sprintf("❌ Error loading config: %v\n", err))
		return 1
	}

	DebugLog(fmt.Sprintf("🔧 User config: %+v", cfg))
	DebugLog(fmt.Sprintf("🔌 Connecting to %s...", cfg.Provider))

	provider, err := llm.NewClient(cfg)
	if err != nil {
		ErrorLog(fmt.Sprintf("❌ Error creating LLM client: %v\n", err))
		return 1
	}

	userInput := Flags.Input
	if userInput == "" {
		ErrorLog("Error: Please provide a user input")
		return 1
	}

	NormalLog("🤖 Processing...\n")
	if Flags.Action == "ask" {
		result, err := agent.AskQuestion(ctx, provider, userInput, cfg)
		if err != nil {
			ErrorLog(fmt.Sprintf("Error asking question: %v\n", err))
			return 1
		}

		NormalLog("%s💡 Answer:%s\n")
		InfoLog(result)
		return 0
	}

	result, err := agent.GenerateCommand(ctx, provider, userInput, cfg)
	if err != nil {
		ErrorLog(fmt.Sprintf("Error generating command: %v\n", err))
		return 1
	}

	NormalLog("💡 Comment:\n")
	InfoLog(fmt.Sprintf("\t%s\n", result.Comment))
	NormalLog("💻 Command:\n")
	InfoLog(fmt.Sprintf("\t%s\n", result.Cmd))

	WarnLog("Execute? [y/N]")
	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		return 0
	}

	input := strings.TrimSpace(strings.ToLower(scanner.Text()))
	if input != "y" && input != "yes" {
		return 0
	}

	if err := executeCommand(stdout, result.Cmd); err != nil {
		return 1
	}

	return 0
}

func executeCommand(stdout io.Writer, command string) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell, "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		ErrorLog(fmt.Sprintf("Execution failed: %v\nnOutput: %s\n", err, string(output)))
		return err
	}

	SuccessLog("Executed successfully\n")
	if len(output) > 0 {
		NormalLog(fmt.Sprintf("Output:\n%s\n", string(output)))
	}

	return nil
}
