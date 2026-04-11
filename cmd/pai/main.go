package pai

import (
	"context"
	"flag"
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
	if *ask {
		agent.AskQuestion(context.Background(), provider, userInput, cfg)
	} else {
		agent.GenerateCommand(context.Background(), provider, userInput, cfg)
	}
}
