package agent

import (
	"context"
	"encoding/json"
	"fmt"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"pai/internal/config"
	"pai/internal/ui"
)

type CmdResult struct {
	Cmd     string `json:"cmd"`
	Comment string `json:"comment"`
}

func GenCMD(ctx context.Context, cfg *config.UserConfig, user_input string) (*CmdResult, error) {
	fmt.Printf("%s %s\n",
		ui.Styles["TagSystem"].Render("[Sys]"),
		ui.Styles["Subdued"].Render("Generating command..."))

	sys_prompt := BuildAgentPrompt(cfg.Prompts["cmd"], "cmd")
	var history = []anyllm.Message{
		{Role: anyllm.RoleSystem, Content: sys_prompt},
		{Role: anyllm.RoleUser, Content: user_input},
	}

	content, _, err := chat(ctx, cfg, cfg.Clients["cmd"], history)
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
