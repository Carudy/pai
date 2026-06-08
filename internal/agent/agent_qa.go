package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/llm"
	"github.com/Carudy/pai/internal/ui"
)

func init() { Register(&QAAgent{}) }

// QAAgent handles question-answering in single-turn or interactive mode.
type QAAgent struct{}

func (a *QAAgent) Name() string        { return "qa" }
func (a *QAAgent) Description() string { return "Question answering (single-turn or interactive)" }

func (a *QAAgent) Run(ctx context.Context, cfg *config.UserConfig, userInput string) error {
	sysPrompt, err := LoadAgentPrompt("qa", cfg.CustomPrompt)
	if err != nil {
		return fmt.Errorf("failed to load qa prompt: %w", err)
	}

	history := []llm.Message{
		{Role: llm.RoleSystem, Content: sysPrompt},
	}
	if userInput != "" {
		history = append(history, llm.Message{Role: llm.RoleUser, Content: userInput})
	}

	for {
		content, newHistory, _, err := chatStr(ctx, cfg, cfg.Clients["qa"], history)
		if err != nil {
			return err
		}
		history = newHistory

		resp, _, err := parseResponseWithRetry(ctx, cfg, cfg.Clients["qa"], content, history)
		if err != nil {
			return err
		}
		answer := resp.GetPayload()

		fmt.Printf("\n%s\n%s",
			ui.RenderStr("TagAgent", "[PAI 🤖]"),
			ui.RenderStr("Cmd", answer))

		if !cfg.Flags.Inter {
			return nil
		}

		// Interactive: wait for next input.
		fmt.Printf("\n")
		next, err := ui.GetUserTextInput("You:")
		if err != nil {
			return fmt.Errorf("user input error: %w", err)
		}
		next = strings.TrimSpace(next)
		if next == "" {
			return nil
		}
		fmt.Printf("%s %s\n", ui.RenderStr("TagUser", "[You]"), ui.RenderStr("Info", next))
		history = append(history, llm.Message{Role: llm.RoleUser, Content: next})
		fmt.Printf("%s\n", ui.RenderStr("Separator", strings.Repeat("\u2500", 40)))
	}
}
