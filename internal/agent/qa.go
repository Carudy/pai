package agent

import (
	"context"
	"fmt"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"pai/internal/config"
	"pai/internal/ui"
)

func QA(ctx context.Context, cfg *config.UserConfig,
	user_input string, multi_turn bool) error {

	fmt.Printf("%s %s\n",
		ui.Styles["TagSystem"].Render("[Sys]"),
		ui.Styles["Subdued"].Render("Thinking..."))
	sys_prompt := BuildAgentPrompt(cfg.Prompts["qa"], "qa")

	var history = []anyllm.Message{
		{Role: anyllm.RoleSystem, Content: sys_prompt},
		{Role: anyllm.RoleUser, Content: user_input},
	}

	// One-turn mode
	if !multi_turn {
		resp, _, err := chat(ctx, cfg, cfg.Clients["qa"], history)

		if err != nil {
			return err
		}

		fmt.Printf("💡 Answer:\n")
		fmt.Printf(ui.Styles["Cmd"].Render(resp))
		return nil
	}

	// Interactive mode
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
			append(history, anyllm.Message{Role: anyllm.RoleUser, Content: user_input}))
		if err != nil {
			return "", err
		}
		history = newHistory
		return resp, nil
	}

	return ui.StartChat(chatFunc, initialMessages)
}
