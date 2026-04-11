package agent

import (
	"context"
	"fmt"
	"pai/internal/userconfig"

	anyllm "github.com/mozilla-ai/any-llm-go"
)

func AskQuestion(ctx context.Context, provider anyllm.Provider, userInput string, cfg *userconfig.Config) (string, error) {
	messages := []anyllm.Message{
		{Role: anyllm.RoleSystem, Content: cfg.AskPrompt},
		{Role: anyllm.RoleUser, Content: userInput},
	}

	resp, err := provider.Completion(ctx, anyllm.CompletionParams{
		Model:    cfg.DefaultModel,
		Messages: messages,
	})
	if err != nil {
		return "", err
	}

	content, ok := resp.Choices[0].Message.Content.(string)
	if !ok {
		return "", fmt.Errorf("unexpected content type")
	}
	return content, nil
}
