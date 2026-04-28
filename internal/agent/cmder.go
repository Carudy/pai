package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/llm"
)

type CmdResult struct {
	Cmd     string `json:"cmd"`
	Comment string `json:"comment"`
}

func GenCMD(ctx context.Context, cfg *config.UserConfig, user_input string) (*CmdResult, error) {

	sys_prompt := BuildAgentPrompt(cfg.Prompts["cmd"], "cmd")
	var history = []llm.Message{
		{Role: llm.RoleSystem, Content: sys_prompt},
		{Role: llm.RoleUser, Content: user_input},
	}

	content, _, err := chatStdout(ctx, cfg, cfg.Clients["cmd"], history)
	if err != nil {
		return nil, err
	}

	jsonStr, err := ExtractJSON(content)
	if err != nil {
		return nil, fmt.Errorf("AI format error: %s", content)
	}

	var result CmdResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("failed to parse command JSON: %w", err)
	}

	return &result, nil
}
