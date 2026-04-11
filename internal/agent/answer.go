package agent

import (
	"context"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"pai/internal/config"
	"pai/internal/prompt"
)

func AskQuestion(ctx context.Context, provider anyllm.Provider, userInput string, cfg *config.Config) (string, error) {
	return completeText(ctx, provider, cfg, prompt.BuildAskSystemPrompt(cfg.AskPrompt), userInput)
}
