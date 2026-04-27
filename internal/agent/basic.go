package agent

import (
	"context"
	"fmt"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"pai/internal/config"
)

func chat(ctx context.Context, provider anyllm.Provider, cfg *config.UserConfig,
	systemPrompt, userInput string, history []anyllm.Message) (string, []anyllm.Message, error) {

	var messages []anyllm.Message
	if len(history) == 0 {
		messages = []anyllm.Message{
			{Role: anyllm.RoleSystem, Content: systemPrompt},
			{Role: anyllm.RoleUser, Content: userInput},
		}
	} else {
		messages = make([]anyllm.Message, len(history), len(history)+1)
		copy(messages, history)
		messages = append(messages, anyllm.Message{Role: anyllm.RoleUser, Content: userInput})
	}

	resp, err := provider.Completion(ctx, anyllm.CompletionParams{
		Model:    cfg.Model,
		Messages: messages,
	})
	if err != nil {
		return "", nil, err
	}
	if len(resp.Choices) == 0 {
		return "", nil, fmt.Errorf("no choices in response")
	}
	content, ok := resp.Choices[0].Message.Content.(string)
	if !ok {
		return "", nil, fmt.Errorf("unexpected content type: %T", resp.Choices[0].Message.Content)
	}

	// Append the assistant reply to build the next history state
	newHistory := append(messages, anyllm.Message{Role: anyllm.RoleAssistant, Content: content})
	return content, newHistory, nil
}
