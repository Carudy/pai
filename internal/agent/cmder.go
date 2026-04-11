package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"pai/internal/userconfig"
	"strings"

	anyllm "github.com/mozilla-ai/any-llm-go"
)

type CmdResult struct {
	Cmd     string `json:"cmd"`
	Comment string `json:"comment"`
}

func extractJSON(content string) (string, error) {
	// Robust extraction: find the first '{' and last '}'
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start == -1 || end == -1 || end < start {
		return "", fmt.Errorf("no JSON found in AI response")
	}
	return content[start : end+1], nil
}

func GenerateCommand(ctx context.Context, provider anyllm.Provider, userInput string, cfg *userconfig.Config) (*CmdResult, error) {
	prompt := fmt.Sprintf("%s\n%s", get_sys_prompt(), cfg.CmdPrompt)
	messages := []anyllm.Message{
		{Role: anyllm.RoleSystem, Content: prompt},
		{Role: anyllm.RoleUser, Content: userInput},
	}

	resp, err := provider.Completion(ctx, anyllm.CompletionParams{
		Model:    cfg.DefaultModel,
		Messages: messages,
	})
	if err != nil {
		return nil, err
	}

	content, ok := resp.Choices[0].Message.Content.(string)
	if !ok {
		return nil, fmt.Errorf("unexpected content type")
	}
	jsonStr, err := extractJSON(content)
	if err != nil {
		return nil, fmt.Errorf("AI format error: %s", content)
	}

	var result CmdResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("failed to parse command JSON: %w", err)
	}

	return &result, nil
}
