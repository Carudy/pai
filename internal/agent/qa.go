package agent

import (
	"context"
	"fmt"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"pai/internal/config"
	"pai/internal/ui"
)

func AskQuestion(ctx context.Context, llm_client anyllm.Provider, cfg *config.UserConfig,
	userInput string, multi_turn bool) error {

	sys_prompt := BuildAgentPrompt(cfg.Prompts["qa"], "qa")

	// One-turn mode
	if !multi_turn {
		resp, _, err := chat(ctx, llm_client, cfg, sys_prompt, userInput, nil)

		if err != nil {
			return err
		}

		fmt.Printf("💡 Answer:\n")
		fmt.Printf(ui.Styles["Cmd"].Render(resp))
		return nil
	}

	// Interactive mode
	var history []anyllm.Message
	var initialMessages []ui.ChatMessage

	if userInput != "" {
		resp, newHistory, err := chat(ctx, llm_client, cfg, sys_prompt, userInput, nil)
		if err != nil {
			return err
		}
		history = newHistory
		initialMessages = []ui.ChatMessage{
			{Role: "user", Content: userInput},
			{Role: "assistant", Content: resp},
		}
	}

	chatFunc := func(input string) (string, error) {
		resp, newHistory, err := chat(ctx, llm_client, cfg, sys_prompt, input, history)
		if err != nil {
			return "", err
		}
		history = newHistory
		return resp, nil
	}

	return ui.StartChat(chatFunc, initialMessages)
}
