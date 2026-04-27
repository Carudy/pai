package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"pai/internal/llm"

	"pai/internal/config"
	"pai/internal/ui"
)

func chatStreamWriter(
	ctx context.Context,
	cfg *config.UserConfig,
	provider llm.Provider,
	history []llm.Message,
	reasoningW io.Writer,
	contentW io.Writer) (string, []llm.Message, error) {

	params := llm.CompletionParams{
		Model:    cfg.Model,
		Messages: history,
		Stream:   true,
	}
	if cfg.Reasoning {
		params.ReasoningEffort = llm.ReasoningEffortHigh
	}

	chunkChan, errChan := provider.CompletionStream(ctx, params)

	var fullContent strings.Builder
	inReasoning := false

	for chunk := range chunkChan {
		for _, choice := range chunk.Choices {
			// ── Reasoning token ─────────────────────────────────────
			if choice.Delta.Reasoning != nil && choice.Delta.Reasoning.Content != "" {
				if reasoningW != nil {
					if !inReasoning {
						fmt.Fprintf(reasoningW, "\n%s ", ui.Styles["Reasoning"].Render("[PAI 🤔]"))
						inReasoning = true
					}
					fmt.Fprint(reasoningW, ui.Styles["Thinking"].Render(choice.Delta.Reasoning.Content))
				}
			}

			// ── Content token ────────────────────────────────────────
			token := choice.Delta.Content
			if token != "" {
				if inReasoning && contentW != nil && reasoningW == contentW {
					// Transition from reasoning → content: print separator.
					fmt.Fprintf(contentW, "\n%s\n", ui.Styles["Separator"].Render(strings.Repeat("─", 40)))
					inReasoning = false
				} else if inReasoning && reasoningW != nil {
					// Separate writers: reasoning only visible, so print
					// separator on reasoning writer to break the block.
					fmt.Fprintf(reasoningW, "\n%s\n", ui.Styles["Separator"].Render(strings.Repeat("─", 40)))
					inReasoning = false
				}
				fullContent.WriteString(token)
				// for now, not show any json content
				// if contentW != nil {
				// 	fmt.Fprint(contentW, token)
				// }
			}
		}
	}

	// Trailing newline after stream ends.
	if contentW != nil {
		fmt.Fprintln(contentW)
	}

	// Drain the error channel.
	if err := <-errChan; err != nil {
		return "", nil, err
	}

	content := fullContent.String()
	newHistory := append(history, llm.Message{Role: llm.RoleAssistant, Content: content})
	return content, newHistory, nil
}

func chat(
	ctx context.Context,
	cfg *config.UserConfig,
	provider llm.Provider,
	history []llm.Message) (string, []llm.Message, error) {

	params := llm.CompletionParams{
		Model:    cfg.Model,
		Messages: history,
	}
	if cfg.Reasoning {
		params.ReasoningEffort = llm.ReasoningEffortHigh
	}

	if cfg.Streaming {
		return chatStreamWriter(ctx, cfg, provider, history, nil, nil)
	}

	resp, err := provider.Completion(ctx, params)
	if err != nil {
		return "", nil, err
	}
	if len(resp.Choices) == 0 {
		return "", nil, fmt.Errorf("no choices in response")
	}
	content := resp.Choices[0].Message.Content

	newHistory := append(history, llm.Message{Role: llm.RoleAssistant, Content: content})
	return content, newHistory, nil
}

func chatStdout(
	ctx context.Context,
	cfg *config.UserConfig,
	provider llm.Provider,
	history []llm.Message) (string, []llm.Message, error) {

	params := llm.CompletionParams{
		Model:    cfg.Model,
		Messages: history,
	}
	if cfg.Reasoning {
		params.ReasoningEffort = llm.ReasoningEffortHigh
	}

	// ── Streaming path ─────────────────────────────────────────────────
	if cfg.Streaming {
		return chatStreamWriter(ctx, cfg, provider, history, os.Stdout, os.Stdout)
	}

	// ── Blocking path ──────────────────────────────────────────────────
	resp, err := provider.Completion(ctx, params)
	if err != nil {
		return "", nil, err
	}
	if len(resp.Choices) == 0 {
		return "", nil, fmt.Errorf("no choices in response")
	}
	content := resp.Choices[0].Message.Content

	// Print reasoning content (non-streaming, so it arrives in one block).
	if cfg.Reasoning && resp.Choices[0].Reasoning != nil && resp.Choices[0].Reasoning.Content != "" {
		fmt.Fprintf(os.Stdout, "\n%s %s\n\n",
			ui.Styles["Reasoning"].Render("🤔"),
			ui.Styles["Reasoning"].Render(resp.Choices[0].Reasoning.Content))
	}

	newHistory := append(history, llm.Message{Role: llm.RoleAssistant, Content: content})
	return content, newHistory, nil
}

func ExtractJSON(content string) (string, error) {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start == -1 || end == -1 || end < start {
		return "", fmt.Errorf("no JSON found in AI response")
	}
	return content[start : end+1], nil
}

func TruncateOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n… [truncated %d bytes]", len(s)-max)
}
