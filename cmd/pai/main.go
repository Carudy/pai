package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"pai/internal/agent"
	"pai/internal/llm"
	"pai/internal/userconfig"
)

const (
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	blue   = "\033[34m"
	cyan   = "\033[36m"
	reset  = "\033[0m"
)

func main() {
	is_debug := flag.Bool("debug", false, "Enable debug mode")
	ask := flag.Bool("ask", false, "Ask a question")
	flag.Parse()

	if *is_debug {
		fmt.Printf("%s🔧 Loading configuration...%s\n", cyan, reset)
	}
	cfg, err := userconfig.LoadConfig()
	if err != nil {
		fmt.Printf("%s❌ Error loading config: %v%s\n", red, err, reset)
		os.Exit(1)
	}

	if *is_debug {
		fmt.Printf("%s🔌 Connecting to %s...%s\n", cyan, cfg.Provider, reset)
	}
	provider, err := llm.NewClient(cfg)
	if err != nil {
		fmt.Printf("%s❌ Error creating LLM client: %v%s\n", red, err, reset)
		os.Exit(1)
	}

	userInput := flag.Arg(0)
	if userInput == "" {
		fmt.Printf("%s❌ Error: Please provide a query%s\n", red, reset)
		os.Exit(1)
	}

	fmt.Printf("%s🤖 Thinking...%s\n", yellow, reset)
	if *ask {
		result, err := agent.AskQuestion(context.Background(), provider, userInput, cfg)
		if err != nil {
			fmt.Printf("%s❌ Error asking question: %v%s\n", red, err, reset)
			os.Exit(1)
		}
		fmt.Printf("%s💡 Answer:%s\n", blue, reset)
		fmt.Println(result)
	} else {
		result, err := agent.GenerateCommand(context.Background(), provider, userInput, cfg)
		if err != nil {
			fmt.Printf("%s❌ Error generating command: %v%s\n", red, err, reset)
			os.Exit(1)
		}

		fmt.Print("💡 Comment:\n")
		fmt.Printf("%s\t%s%s\n", blue, result.Comment, reset)
		fmt.Print("💻 Command:\n")
		fmt.Printf("%s\t%s\n", yellow, result.Cmd)

		// Ask to execute
		fmt.Printf("%sExecute? [y/N]: %s", cyan, reset)
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			input := strings.TrimSpace(strings.ToLower(scanner.Text()))
			if input == "y" || input == "yes" {
				shell := os.Getenv("SHELL")
				if shell == "" {
					shell = "/bin/sh"
				}

				cmd := exec.Command(shell, "-c", result.Cmd)
				output, err := cmd.CombinedOutput()
				if err != nil {
					fmt.Printf("%s❌ Execution failed: %v\nOutput: %s%s\n", red, err, string(output), reset)
				} else {
					fmt.Printf("%s✅ Executed successfully%s\n", green, reset)
					if len(output) > 0 {
						fmt.Printf("Output:\n%s\n", string(output))
					}
				}
			}
		}
	}
}
