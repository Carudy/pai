package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/llm"
	"github.com/Carudy/pai/internal/ui"
)

// ---------------------------------------------------------------------------
// chatOpts
// ---------------------------------------------------------------------------

// chatOpts configures a chat call. All agents enforce JSON output
// (response_format: json_object), so that field is always set internally.
type chatOpts struct {
	Stream    bool
	OutputW   io.Writer    // where to print the final answer (nil = suppress)
	ReasonW   io.Writer    // where to print reasoning tokens (nil = suppress)
	OnToken   func(string) // per-token callback for streaming (nil = suppress)
	Reasoning bool         // whether the LLM was asked to reason (cfg.Reasoning)
}

// ---------------------------------------------------------------------------
// Core chat
// ---------------------------------------------------------------------------

// chat sends messages to the LLM, returns the parsed assistant JSON string.
// It always requests JSON mode; the caller uses ExtractJSON on the result.
func chat(
	ctx context.Context,
	cfg *config.UserConfig,
	provider llm.Provider,
	history []llm.Message,
	opts chatOpts,
) (content string, newHistory []llm.Message, err error) {

	params := llm.CompletionParams{
		Model:          cfg.Model,
		Messages:       history,
		Stream:         opts.Stream,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}

	opts.Reasoning = cfg.Reasoning
	if cfg.Reasoning {
		params.ReasoningEffort = llm.ReasoningEffortHigh
	}

	var resp *llm.ChatCompletion

	if opts.Stream {
		content, err = doStream(ctx, provider, params, opts.ReasonW, opts.OnToken, opts.Reasoning)
	} else {
		resp, err = provider.Completion(ctx, params)
	}
	if err != nil {
		return "", nil, err
	}

	// Blocking path.
	if resp != nil {
		if len(resp.Choices) == 0 {
			return "", nil, fmt.Errorf("no choices in response")
		}
		content = resp.Choices[0].Message.Content

		// Print reasoning content (non-streaming, arrives in one block).
		// Always print to stdout when reasoning is enabled, regardless of opts.
		if opts.Reasoning &&
			resp.Choices[0].Reasoning != nil && resp.Choices[0].Reasoning.Content != "" {
			fmt.Fprintf(os.Stdout, "\n%s %s\n\n",
				ui.Styles["Reasoning"].Render("🤔"),
				ui.Styles["Reasoning"].Render(resp.Choices[0].Reasoning.Content))
		}
	}

	newHistory = append(history, llm.Message{Role: llm.RoleAssistant, Content: content})
	return content, newHistory, nil
}

// ---------------------------------------------------------------------------
// Convenience wrappers
// ---------------------------------------------------------------------------

// chatStr returns JSON as a string (no terminal output for answer, but
// reasoning will still be printed to stdout when cfg.Reasoning is true).
func chatStr(ctx context.Context, cfg *config.UserConfig, provider llm.Provider, history []llm.Message) (string, []llm.Message, error) {
	return chat(ctx, cfg, provider, history, chatOpts{
		Stream: cfg.Streaming,
	})
}

// chatStdout prints the final answer to stdout (respects cfg.Streaming).
func chatStdout(ctx context.Context, cfg *config.UserConfig, provider llm.Provider, history []llm.Message) (string, []llm.Message, error) {
	return chat(ctx, cfg, provider, history, chatOpts{
		Stream:  cfg.Streaming,
		OutputW: os.Stdout,
		ReasonW: os.Stdout,
	})
}

// ---------------------------------------------------------------------------
// Streaming implementation
// ---------------------------------------------------------------------------

func doStream(
	ctx context.Context,
	provider llm.Provider,
	params llm.CompletionParams,
	reasonW io.Writer,
	onToken func(string),
	reasoning bool,
) (string, error) {
	chunkChan, errChan := provider.CompletionStream(ctx, params)

	var fullContent strings.Builder
	inReasoning := false

	for chunk := range chunkChan {
		for _, choice := range chunk.Choices {
			// Reasoning tokens.
			if choice.Delta.Reasoning != nil && choice.Delta.Reasoning.Content != "" {
				// Determine where to write reasoning: prefer reasonW, fall back to
				// onToken (so TUI displays it), and if neither is available but
				// reasoning is enabled, write to stdout directly.
				w := reasonW
				if w == nil && onToken != nil {
					// Forward reasoning tokens as onToken callbacks too, so the
					// TUI/renderer can display them alongside content.
					onToken(choice.Delta.Reasoning.Content)
				}
				if w == nil && reasoning {
					w = os.Stdout
				}

				if w != nil {
					if !inReasoning {
						fmt.Fprintf(w, "\n%s ", ui.Styles["Reasoning"].Render("[PAI 🤔]"))
						inReasoning = true
					}
					fmt.Fprint(w, ui.Styles["Thinking"].Render(choice.Delta.Reasoning.Content))
				}
			}

			// Content tokens (JSON is captured; caller decides whether to display).
			if token := choice.Delta.Content; token != "" {
				if inReasoning && (reasonW != nil || (reasonW == nil && reasoning)) {
					w := reasonW
					if w == nil {
						w = os.Stdout
					}
					fmt.Fprintf(w, "\n%s\n", ui.Styles["Separator"].Render(strings.Repeat("─", 40)))
					inReasoning = false
				}
				fullContent.WriteString(token)
				if onToken != nil {
					onToken(token)
				}
			}
		}
	}

	if err := <-errChan; err != nil {
		return "", err
	}

	return fullContent.String(), nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ExtractJSON extracts the first JSON object from a string.
func ExtractJSON(content string) (string, error) {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start == -1 || end == -1 || end < start {
		return "", fmt.Errorf("no JSON found in AI response")
	}
	return content[start : end+1], nil
}

// TruncateOutput truncates a string to max bytes, appending a notice.
func TruncateOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n… [truncated %d bytes]", len(s)-max)
}
