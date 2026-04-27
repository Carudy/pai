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

// chatStreamWriter performs streaming completion. Reasoning tokens are
// always written to reasoningW (if non-nil). Content tokens are written
// to contentW (if non-nil). When both are non-nil a separator is printed
// between reasoning and visible content.
//
// If stopSpinner is non-nil, it is called once when the first meaningful
// token (reasoning or content) arrives, allowing a pre-stream spinner to
// be dismissed.
//
// Returns the accumulated visible content and the updated message history.
func chatStreamWriter(ctx context.Context, cfg *config.UserConfig,
	provider llm.Provider, history []llm.Message,
	reasoningW io.Writer, contentW io.Writer,
	stopSpinner func()) (string, []llm.Message, error) {

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
	spinnerStopped := false
	stopSpinnerOnce := func() {
		if !spinnerStopped && stopSpinner != nil {
			spinnerStopped = true
			stopSpinner()
		}
	}

	for chunk := range chunkChan {
		for _, choice := range chunk.Choices {
			// ── Reasoning token ─────────────────────────────────────
			if choice.Delta.Reasoning != nil && choice.Delta.Reasoning.Content != "" {
				stopSpinnerOnce()
				if reasoningW != nil {
					if !inReasoning {
						fmt.Fprintf(reasoningW, "\n%s ", ui.Styles["Reasoning"].Render("🤔"))
						inReasoning = true
					}
					fmt.Fprint(reasoningW, ui.Styles["Reasoning"].Render(choice.Delta.Reasoning.Content))
				}
			}

			// ── Content token ────────────────────────────────────────
			token := choice.Delta.Content
			if token != "" {
				stopSpinnerOnce()
				// Transition from reasoning → content: print separator.
				if inReasoning && contentW != nil && reasoningW == contentW {
					fmt.Fprintf(contentW, "\n%s\n", ui.Styles["Separator"].Render(strings.Repeat("─", 40)))
					inReasoning = false
				} else if inReasoning && reasoningW != nil {
					// Separate writers: reasoning only visible, so print
					// separator on reasoning writer to break the block.
					fmt.Fprintf(reasoningW, "\n%s\n", ui.Styles["Separator"].Render(strings.Repeat("─", 40)))
					inReasoning = false
				}
				fullContent.WriteString(token)
				if contentW != nil {
					fmt.Fprint(contentW, token)
				}
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
		return chatStreamWriter(ctx, cfg, provider, history, nil, nil, nil)
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

// chatStdout streams the LLM response with appropriate terminal visibility.
//
// When showContent is true (QA agent), content tokens are written to stdout
// in real time alongside reasoning tokens.
//
// When showContent is false (cmd / devops agents), only reasoning tokens are
// displayed on the terminal; the raw content (e.g. JSON) is collected silently
// so that the caller can render its own structured output afterwards.
//
// When streaming is disabled, the entire response arrives at once. In that
// case reasoning is printed (if any) and content is always returned to the
// caller. The showContent flag only affects whether raw tokens appear during
// streaming.
func chatStdout(ctx context.Context, cfg *config.UserConfig,
	provider llm.Provider, history []llm.Message,
	stopSpinner func(), showContent bool) (string, []llm.Message, error) {

	params := llm.CompletionParams{
		Model:    cfg.Model,
		Messages: history,
	}
	if cfg.Reasoning {
		params.ReasoningEffort = llm.ReasoningEffortHigh
	}

	// ── Streaming path ─────────────────────────────────────────────────
	if cfg.Streaming {
		if showContent {
			// QA agent: display both reasoning and content on stdout.
			return chatStreamWriter(ctx, cfg, provider, history, os.Stdout, os.Stdout, stopSpinner)
		}
		// CMD / devops: display reasoning only; suppress raw content.
		return chatStreamWriter(ctx, cfg, provider, history, os.Stdout, nil, stopSpinner)
	}

	// ── Blocking path ──────────────────────────────────────────────────
	resp, err := provider.Completion(ctx, params)
	if err != nil {
		if stopSpinner != nil {
			stopSpinner()
		}
		return "", nil, err
	}
	if len(resp.Choices) == 0 {
		if stopSpinner != nil {
			stopSpinner()
		}
		return "", nil, fmt.Errorf("no choices in response")
	}
	content := resp.Choices[0].Message.Content

	if stopSpinner != nil {
		stopSpinner()
	}

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
