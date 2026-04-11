package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"pai/internal/agent"
	"pai/internal/llm"
	"pai/internal/userconfig"
)

func main() {
	ask := flag.Bool("ask", false, "Ask a question")
	flag.Parse()

	cfg, err := userconfig.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	provider, err := llm.NewClient(cfg)
	if err != nil {
		log.Fatalf("Failed to create LLM client: %v", err)
	}

	userInput := flag.Arg(0)
	if userInput == "" {
		log.Fatal("Please provide a query")
	}

	if *ask {
		result, err := agent.AskQuestion(context.Background(), provider, userInput, cfg)
		if err != nil {
			log.Fatalf("Failed to ask question: %v", err)
		}
		fmt.Println(result)
	} else {
		result, err := agent.GenerateCommand(context.Background(), provider, userInput, cfg)
		if err != nil {
			log.Fatalf("Failed to generate command: %v", err)
		}
		fmt.Printf("Command: %s\nComment: %s\n", result.Cmd, result.Comment)
	}
}
