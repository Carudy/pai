package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"pai/internal/agent"
	"pai/internal/config"
	"pai/internal/llm"
)

const (
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	blue   = "\033[34m"
	cyan   = "\033[36m"
	reset  = "\033[0m"
)

func Run(ctx context.Context, stdin io.Reader, stdout io.Writer, args []string) int {
	fs := flag.NewFlagSet("pai", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	debug := fs.Bool("debug", false, "Enable debug mode")
	ask := fs.Bool("ask", false, "Ask a question")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stdout, "%s❌ %v%s\n", red, err, reset)
		return 2
	}

	if *debug {
		fmt.Fprintf(stdout, "%s🔧 Loading configuration...%s\n", cyan, reset)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "%s❌ Error loading config: %v%s\n", red, err, reset)
		return 1
	}

	if *debug {
		fmt.Fprintf(stdout, "%s🔌 Connecting to %s...%s\n", cyan, cfg.Provider, reset)
	}
	provider, err := llm.NewClient(cfg)
	if err != nil {
		fmt.Fprintf(stdout, "%s❌ Error creating LLM client: %v%s\n", red, err, reset)
		return 1
	}

	userInput := fs.Arg(0)
	if userInput == "" {
		fmt.Fprintf(stdout, "%s❌ Error: Please provide a query%s\n", red, reset)
		return 1
	}

	fmt.Fprintf(stdout, "%s🤖 Thinking...%s\n", yellow, reset)
	if *ask {
		result, err := agent.AskQuestion(ctx, provider, userInput, cfg)
		if err != nil {
			fmt.Fprintf(stdout, "%s❌ Error asking question: %v%s\n", red, err, reset)
			return 1
		}

		fmt.Fprintf(stdout, "%s💡 Answer:%s\n", blue, reset)
		fmt.Fprintln(stdout, result)
		return 0
	}

	result, err := agent.GenerateCommand(ctx, provider, userInput, cfg)
	if err != nil {
		fmt.Fprintf(stdout, "%s❌ Error generating command: %v%s\n", red, err, reset)
		return 1
	}

	fmt.Fprint(stdout, "💡 Comment:\n")
	fmt.Fprintf(stdout, "%s\t%s%s\n", blue, result.Comment, reset)
	fmt.Fprint(stdout, "💻 Command:\n")
	fmt.Fprintf(stdout, "%s\t%s\n", yellow, result.Cmd)

	fmt.Fprintf(stdout, "%sExecute? [y/N]: %s", cyan, reset)
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
		fmt.Fprintf(stdout, "%s❌ Execution failed: %v\nOutput: %s%s\n", red, err, string(output), reset)
		return err
	}

	fmt.Fprintf(stdout, "%s✅ Executed successfully%s\n", green, reset)
	if len(output) > 0 {
		fmt.Fprintf(stdout, "Output:\n%s\n", string(output))
	}

	return nil
}
