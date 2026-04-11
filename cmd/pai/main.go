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

// func saveHistory(cmd string) {
// 	homeDir, err := os.UserHomeDir()
// 	if err != nil {
// 		return
// 	}
// 	// historyPath := filepath.Join(homeDir, ".pai_history")
// 	// file, err := os.OpenFile(historyPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
// 	// if err != nil {
// 	// 	return
// 	// }
// 	// defer file.Close()
// 	// timestamp := time.Now().Format("2006-01-02 15:04:05")
// 	// fmt.Fprintf(file, "[%s] %s\n", timestamp, cmd)

// 	// Also add to shell history
// 	shell := os.Getenv("SHELL")
// 	if strings.Contains(shell, "bash") {
// 		histFilePath := filepath.Join(homeDir, ".bash_history")
// 		histFile, err := os.OpenFile(histFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
// 		if err == nil {
// 			defer histFile.Close()
// 			fmt.Fprintln(histFile, cmd)
// 		}
// 	} else if strings.Contains(shell, "zsh") {
// 		histFilePath := filepath.Join(homeDir, ".zsh_history")
// 		histFile, err := os.OpenFile(histFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
// 		if err == nil {
// 			defer histFile.Close()
// 			fmt.Fprintln(histFile, cmd)
// 		}
// 	} else if strings.Contains(shell, "fish") {
// 		// Fish uses YAML-like format
// 		configDir := os.Getenv("XDG_CONFIG_HOME")
// 		if configDir == "" {
// 			configDir = filepath.Join(homeDir, ".config")
// 		}
// 		histFilePath := filepath.Join(configDir, "fish", "fish_history")
// 		histFile, err := os.OpenFile(histFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
// 		if err == nil {
// 			defer histFile.Close()
// 			timestamp := time.Now().Unix()
// 			fmt.Fprintf(histFile, "- cmd: %s\n  when: %d\n  paths:\n", cmd, timestamp)
// 		}
// 	}
// }

func main() {
	ask := flag.Bool("ask", false, "Ask a question")
	flag.Parse()

	fmt.Printf("%s🔧 Loading configuration...%s\n", cyan, reset)
	cfg, err := userconfig.LoadConfig()
	if err != nil {
		fmt.Printf("%s❌ Error loading config: %v%s\n", red, err, reset)
		os.Exit(1)
	}
	// fmt.Printf("%s✅ Configuration loaded.%s\n", green, reset)

	fmt.Printf("%s🔌 Connecting to %s...%s\n", cyan, cfg.Provider, reset)
	provider, err := llm.NewClient(cfg)
	if err != nil {
		fmt.Printf("%s❌ Error creating LLM client: %v%s\n", red, err, reset)
		os.Exit(1)
	}
	// fmt.Printf("%s✅ Connected to LLM provider.%s\n", green, reset)

	userInput := flag.Arg(0)
	if userInput == "" {
		fmt.Printf("%s❌ Error: Please provide a query%s\n", red, reset)
		os.Exit(1)
	}

	if *ask {
		fmt.Printf("%s🤖 Thinking...%s\n", yellow, reset)
		result, err := agent.AskQuestion(context.Background(), provider, userInput, cfg)
		if err != nil {
			fmt.Printf("%s❌ Error asking question: %v%s\n", red, err, reset)
			os.Exit(1)
		}
		fmt.Printf("%s💡 Answer:%s\n", blue, reset)
		fmt.Println(result)
	} else {
		fmt.Printf("%s🤖 Thinking...%s\n", yellow, reset)
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
				cmd := exec.Command("/bin/sh", "-c", result.Cmd)
				output, err := cmd.CombinedOutput()
				if err != nil {
					fmt.Printf("%s❌ Execution failed: %v\nOutput: %s%s\n", red, err, string(output), reset)
				} else {
					fmt.Printf("%s✅ Executed successfully%s\n", green, reset)
					if len(output) > 0 {
						fmt.Printf("Output:\n%s\n", string(output))
					}
					// saveHistory(result.Cmd)
				}
			}
		}
	}
}
