package agent

import (
	"context"
	"fmt"
	"pai/internal/configs"
	"pai/internal/userconfig"

	anyllm "github.com/mozilla-ai/any-llm-go"
)

func AskQuestion(ctx context.Context, provider anyllm.Provider, userInput string, cfg *userconfig.Config) (string, error) {
	prompt := fmt.Sprintf("%s\n%s", cfg.AskPrompt, configs.Get_sys_prompt())

	messages := []anyllm.Message{
		{Role: anyllm.RoleSystem, Content: prompt},
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
