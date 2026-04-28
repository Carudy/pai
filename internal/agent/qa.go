package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/llm"
	"github.com/Carudy/pai/internal/ui"
)

// qaResponse is the expected JSON envelope for the QA agent.
type qaResponse struct {
	Answer string `json:"answer"`
}

func QA(ctx context.Context, cfg *config.UserConfig, userInput string) error {
	sysPrompt := BuildAgentPrompt(cfg.Prompts["qa"], "qa")
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: sysPrompt},
		{Role: llm.RoleUser, Content: userInput},
	}

	// ── One-turn mode (stdout) ─────────────────────────────────────────
	if !cfg.Flags.Inter {
		content, _, err := chatStdout(ctx, cfg, cfg.Clients["qa"], history)
		if err != nil {
			return err
		}
		fmt.Printf("%s\n%s", ui.Styles["TagAgent"].Render("[PAI 🤖]:"), ui.Styles["Cmd"].Render(extractAnswer(content)))
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

// extractAnswer parses the JSON envelope and returns just the answer text.
// If parsing fails, returns the raw content as a fallback.
func extractAnswer(raw string) string {
	jsonStr, err := ExtractJSON(raw)
	if err != nil {
		return raw
	}
	var resp qaResponse
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return raw
	}
	return resp.Answer
}
