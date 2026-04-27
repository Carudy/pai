package agent

import (
	"context"
	"fmt"
	"strings"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"pai/internal/config"
)

func chat(ctx context.Context, cfg *config.UserConfig,
	provider *anyllm.Provider, history []anyllm.Message) (string, []anyllm.Message, error) {

	resp, err := (*provider).Completion(ctx, anyllm.CompletionParams{
		Model:    cfg.Model,
		Messages: history,
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
	newHistory := append(history, anyllm.Message{Role: anyllm.RoleAssistant, Content: content})
	return content, newHistory, nil
}

func ExtractJSON(content string) (string, error) {
	// Robust extraction: find the first '{' and last '}'
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start == -1 || end == -1 || end < start {
		return "", fmt.Errorf("no JSON found in AI response")
	}
	return content[start : end+1], nil
}

func TruncateOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n… [truncated %d bytes]", len(s)-max)
}
