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

// chatStreamWriter performs streaming completion. Each content token is
// written to w (if non-nil). When cfg.Reasoning is true, reasoning tokens
// are displayed with a "🤔" prefix and the Reasoning style, followed by a
// separator when the visible response begins.
//
// Returns the accumulated visible content and the updated message history.
func chatStreamWriter(ctx context.Context, cfg *config.UserConfig,
	provider llm.Provider, history []llm.Message, w io.Writer) (string, []llm.Message, error) {

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
				if w != nil {
					if !inReasoning {
						fmt.Fprintf(w, "\n%s ", ui.Styles["Reasoning"].Render("🤔"))
						inReasoning = true
					}
					fmt.Fprint(w, ui.Styles["Reasoning"].Render(choice.Delta.Reasoning.Content))
				}
			}

			// ── Content token ────────────────────────────────────────
			token := choice.Delta.Content
			if token != "" {
				// Transition from reasoning → content: print separator.
				if inReasoning && w != nil {
					fmt.Fprintf(w, "\n%s\n", ui.Styles["Separator"].Render(strings.Repeat("─", 40)))
					inReasoning = false
				}
				fullContent.WriteString(token)
				if w != nil {
					fmt.Fprint(w, token)
				}
			}
		}
	}

	// Trailing newline after stream ends.
	if w != nil {
		fmt.Fprintln(w)
	}

	// Drain the error channel.
	if err := <-errChan; err != nil {
		return "", nil, err
	}

	content := fullContent.String()
	newHistory := append(history, llm.Message{Role: llm.RoleAssistant, Content: content})
	return content, newHistory, nil
}

// chat performs a chat completion. When cfg.Streaming is true and the
// provider supports it, tokens are collected silently without any terminal
// output (the caller manages its own display, e.g. the TUI chat).
// Otherwise it uses the blocking Completion call.
//
// Returns the full response content and the updated message history.
func chat(ctx context.Context, cfg *config.UserConfig,
	provider llm.Provider, history []llm.Message) (string, []llm.Message, error) {

	params := llm.CompletionParams{
		Model:    cfg.Model,
		Messages: history,
	}
	if cfg.Reasoning {
		params.ReasoningEffort = llm.ReasoningEffortHigh
	}

	if cfg.Streaming {
		return chatStreamWriter(ctx, cfg, provider, history, nil)
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

// chatStdout is a convenience wrapper that streams tokens to os.Stdout in
// real time when cfg.Streaming is true. For non-streaming completions it
// still prints reasoning content (if any) so the user always sees the
// "thinking" output.
//
// Use this for agents that render their own structured output *after* the
// LLM response (cmd, devops, QA single-turn). Use chat() directly for the
// TUI multi-turn chat which manages its own display.
func chatStdout(ctx context.Context, cfg *config.UserConfig,
	provider llm.Provider, history []llm.Message) (string, []llm.Message, error) {

	params := llm.CompletionParams{
		Model:    cfg.Model,
		Messages: history,
	}
	if cfg.Reasoning {
		params.ReasoningEffort = llm.ReasoningEffortHigh
	}

	// Streaming path — chatStreamWriter handles reasoning display inline.
	if cfg.Streaming {
		return chatStreamWriter(ctx, cfg, provider, history, os.Stdout)
	}

	// Blocking path — do the call directly so we can inspect reasoning.
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

// ExtractJSON finds the first '{' and last '}' in content and returns the
// substring between them. Used to extract a JSON object from an LLM response
// that may include surrounding commentary.
func ExtractJSON(content string) (string, error) {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start == -1 || end == -1 || end < start {
		return "", fmt.Errorf("no JSON found in AI response")
	}
	return content[start : end+1], nil
}

// TruncateOutput shortens a string to max bytes, appending a truncation
// notice if the original was longer.
func TruncateOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n… [truncated %d bytes]", len(s)-max)
}
