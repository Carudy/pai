// pai/internal/agent/qa.go

package agent

import (
	"context"
	"fmt"

	"pai/internal/llm"

	"pai/internal/config"
	"pai/internal/ui"
)

func QA(
	ctx context.Context,
	cfg *config.UserConfig,
	user_input string,
	multi_turn bool) error {

	sys_prompt := BuildAgentPrompt(cfg.Prompts["qa"], "qa")

	var history = []llm.Message{
		{Role: llm.RoleSystem, Content: sys_prompt},
		{Role: llm.RoleUser, Content: user_input},
	}

	// One-turn mode
	if !multi_turn {
		resp, _, err := chatStdout(ctx, cfg, cfg.Clients["qa"], history)
		if err != nil {
			return err
		}

		fmt.Printf("%s\n%s", ui.Styles["TagAgent"].Render("[PAI 🤖]:"), ui.Styles["Cmd"].Render(resp))
		return nil
	}

	// Multi-turn mode
	var initialMessages []ui.ChatMessage
	if user_input != "" {
		resp, newHistory, err := chat(ctx, cfg, cfg.Clients["qa"], history)
		if err != nil {
			return err
		}
		history = newHistory
		initialMessages = []ui.ChatMessage{
			{Role: "user", Content: user_input},
			{Role: "assistant", Content: resp},
		}
	}

	chatFunc := func(input string) (string, error) {
		resp, newHistory, err := chat(ctx, cfg, cfg.Clients["qa"],
			append(history, llm.Message{Role: llm.RoleUser, Content: input}))
		if err != nil {
			return "", err
		}
		history = newHistory
		return resp, nil
	}

	return ui.StartChat(chatFunc, initialMessages)
}
