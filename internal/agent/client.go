package agent

import (
	"context"
	"fmt"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"pai/internal/config"
)

func completeText(ctx context.Context, provider anyllm.Provider, cfg *config.Config, systemPrompt, userInput string) (string, error) {
	messages := []anyllm.Message{
		{Role: anyllm.RoleSystem, Content: systemPrompt},
		{Role: anyllm.RoleUser, Content: userInput},
	}

	resp, err := provider.Completion(ctx, anyllm.CompletionParams{
		Model:    cfg.DefaultModel,
		Messages: messages,
	})
	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	content, ok := resp.Choices[0].Message.Content.(string)
	if !ok {
		return "", fmt.Errorf("unexpected content type")
	}

	return content, nil
}
