package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"pai/internal/config"
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

func GenerateCommand(ctx context.Context, provider anyllm.Provider, userInput string, cfg *config.UserConfig) (*CmdResult, error) {

	sys_prompt := BuildAgentPrompt(cfg.Prompts["cmder"], "cmder")

	content, _, err := chat(ctx, provider, cfg, sys_prompt, userInput, nil)
	if err != nil {
		return nil, err
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
