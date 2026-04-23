package agent

import (
	"context"
	"fmt"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"pai/internal/config"
	"pai/internal/prompt"
	"pai/internal/ui"
)

func AskQuestion(ctx context.Context, llm_client anyllm.Provider, cfg *config.UserConfig,
	userInput string, multi_turn bool) error {

	sys_prompt := prompt.BuildAskSystemPrompt(cfg.AskPrompt)

	if !multi_turn {
		resp, _, err := chat(ctx, llm_client, cfg, sys_prompt, userInput, nil)

		if err != nil {
			return err
		}

		fmt.Printf("💡 Answer:\n")
		fmt.Printf(ui.Styles.Cmd.Render(resp))
		return nil
	}

	// // interactive mode
	// history := nil
	// for {
	// 	resp, history, err := chat(ctx, llm_client, cfg, sys_prompt, userInput, history)
	// 	if err != nil {
	// 		return err
	// 	}

	// }

	return nil
}
