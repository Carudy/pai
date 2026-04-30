package agent

import (
	"context"
	"fmt"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/llm"
	"github.com/Carudy/pai/internal/ui"
)

func QA(ctx context.Context, cfg *config.UserConfig, userInput string) error {
	sysPrompt, err := LoadAgentPrompt("qa", cfg.CustomPrompt)
	if err != nil {
		return fmt.Errorf("failed to load qa prompt: %w", err)
	}

	history := []llm.Message{
		{Role: llm.RoleSystem, Content: sysPrompt},
		{Role: llm.RoleUser, Content: userInput},
	}

	// ── One-turn mode (stdout) ─────────────────────────────────────────
	if !cfg.Flags.Inter {
		content, history, err := chatStr(ctx, cfg, cfg.Clients["qa"], history)
		if err != nil {
			return err
		}
		resp, _, err := parseResponseWithRetry(ctx, cfg, cfg.Clients["qa"], content, history)
		if err != nil {
			return err
		}
		answer, err := resp.GetPayload()
		if err != nil {
			return err
		}
		fmt.Printf("%s\n%s", ui.Styles["TagAgent"].Render("[PAI 🤖]:"), ui.Styles["Cmd"].Render(answer))
		return nil
	}

	// ── Multi-turn mode (streaming chatbox) ────────────────────────────
	var initialMessages []ui.ChatMessage
	if userInput != "" {
		resp, newHistory, err := chatStr(ctx, cfg, cfg.Clients["qa"], history)
		if err != nil {
			return err
		}
		history = newHistory
		initialMessages = []ui.ChatMessage{
			{Role: "user", Content: userInput},
			{Role: "assistant", Content: extractAnswer(resp)},
		}
	}

	// StreamChatFunc: streams raw JSON tokens to the chatbox for
	// incremental display, then returns the extracted answer.
	streamFunc := ui.StreamChatFunc(func(ctx context.Context, input string, onToken func(string)) (string, error) {
		newHistory := append(history, llm.Message{Role: llm.RoleUser, Content: input})
		fullJSON, hist, err := chat(ctx, cfg, cfg.Clients["qa"], newHistory, chatOpts{
			Stream:  true,
			OnToken: onToken, // raw JSON tokens streamed to chatbox
		})
		if err != nil {
			return "", err
		}
		history = hist
		return extractAnswer(fullJSON), nil
	})

	return ui.StartStreamChat(ctx, streamFunc, initialMessages)
}

// extractAnswer parses the unified agent JSON envelope and returns the payload
// text. Falls back to returning raw content if parsing fails.
func extractAnswer(raw string) string {
	resp, err := ParseAgentResponse(raw)
	if err != nil {
		return raw
	}
	answer, err := resp.GetPayload()
	if err != nil {
		return raw
	}
	return answer
}
