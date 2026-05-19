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
	Stream  bool
	ReasonW io.Writer        // where to print reasoning tokens (nil → stdout when reasoning enabled)
	OnToken func(string)     // per-token callback for streaming (nil = suppress)
	OnUsage func(*llm.Usage) // optional usage callback (for token display)
}

// ---------------------------------------------------------------------------
// Core chat
// ---------------------------------------------------------------------------

// chat sends messages to the LLM, returns the parsed assistant JSON string
// and token usage info. It always requests JSON mode; the caller uses
// ExtractJSON on the result.
func chat(
	ctx context.Context,
	cfg *config.UserConfig,
	provider llm.Provider,
	history []llm.Message,
	opts chatOpts,
) (content string, newHistory []llm.Message, usage *llm.Usage, err error) {

	params := llm.CompletionParams{
		Model:           cfg.Model,
		Messages:        history,
		Stream:          opts.Stream,
		ResponseFormat:  &llm.ResponseFormat{Type: "json_object"},
		ReasoningEffort: cfg.ReasoningEffort,
	}

	if opts.Stream {
		content, usage, err = doStream(ctx, provider, params,
			opts.ReasonW, opts.OnToken,
			cfg.ReasoningEffort != llm.ReasoningEffortNone)
	} else {
		var resp *llm.ChatCompletion
		resp, err = provider.Completion(ctx, params)
		if err == nil && resp != nil {
			content, usage = extractCompletion(resp, cfg)
		}
	}
	if err != nil {
		return "", nil, nil, err
	}

	newHistory = append(history, llm.Message{Role: llm.RoleAssistant, Content: content})
	return content, newHistory, usage, nil
}

// extractCompletion unpacks a non-streaming response, prints reasoning if
// enabled, and returns content + usage.
func extractCompletion(resp *llm.ChatCompletion, cfg *config.UserConfig) (string, *llm.Usage) {
	if len(resp.Choices) == 0 {
		return "", resp.Usage
	}
	content := resp.Choices[0].Message.Content

	// Print reasoning content (non-streaming, arrives in one block).
	if cfg.ReasoningEffort != llm.ReasoningEffortNone &&
		resp.Choices[0].Reasoning != nil && resp.Choices[0].Reasoning.Content != "" {
		fmt.Fprintf(os.Stdout, "\n%s %s\n\n",
			ui.Styles["Reasoning"].Render("\U0001f914"),
			ui.Styles["Reasoning"].Render(resp.Choices[0].Reasoning.Content))
	}
	return content, resp.Usage
}

// ---------------------------------------------------------------------------
// Convenience wrappers
// ---------------------------------------------------------------------------

// chatStr returns JSON as a string (no terminal output for answer, but
// reasoning will still be printed to stdout when cfg.Reasoning is true).
func chatStr(ctx context.Context, cfg *config.UserConfig, provider llm.Provider, history []llm.Message) (string, []llm.Message, *llm.Usage, error) {
	return chat(ctx, cfg, provider, history, chatOpts{
		Stream: cfg.Streaming,
	})
}

// ---------------------------------------------------------------------------
// Streaming implementation
// ---------------------------------------------------------------------------

// streamState holds the mutable state during a streaming chat call.
type streamState struct {
	fullContent strings.Builder
	usage       *llm.Usage
}

// doStream reads streaming chunks from the provider, routes reasoning output,
// collects content tokens, and tracks usage. Returns the full content and
// the last usage block seen.
func doStream(
	ctx context.Context,
	provider llm.Provider,
	params llm.CompletionParams,
	reasonW io.Writer,
	onToken func(string),
	reasoning bool,
) (string, *llm.Usage, error) {
	chunkChan, errChan := provider.CompletionStream(ctx, params)

	st := &streamState{}
	rw := newReasoningRouter(reasonW, onToken, reasoning)
	inReasoning := false

	for chunk := range chunkChan {
		// Track usage (typically arrives in the final chunk).
		if chunk.Usage != nil {
			st.usage = chunk.Usage
		}

		for _, choice := range chunk.Choices {
			// Reasoning tokens.
			if choice.Delta.Reasoning != nil && choice.Delta.Reasoning.Content != "" {
				if !inReasoning {
					rw.writePrefix()
					inReasoning = true
				}
				rw.writeToken(choice.Delta.Reasoning.Content)
			}

			// Content tokens.
			if token := choice.Delta.Content; token != "" {
				if inReasoning {
					rw.writeSeparator()
					inReasoning = false
				}
				st.fullContent.WriteString(token)
				if onToken != nil {
					onToken(token)
				}
			}
		}
	}

	if err := <-errChan; err != nil {
		return "", nil, err
	}

	return st.fullContent.String(), st.usage, nil
}

// ---------------------------------------------------------------------------
// Reasoning output routing
// ---------------------------------------------------------------------------

// reasoningRouter decides where to send reasoning tokens.  It prefers
// reasonW (explicit writer), falls back to onToken (TUI), and finally
// falls back to os.Stdout when reasoning mode is enabled.
type reasoningRouter struct {
	w        io.Writer    // explicit writer (highest priority)
	onToken  func(string) // TUI callback
	fallback bool         // send to os.Stdout as last resort
}

func newReasoningRouter(reasonW io.Writer, onToken func(string), reasoning bool) *reasoningRouter {
	rr := &reasoningRouter{fallback: reasoning}
	// Priority: reasonW > onToken > stdout
	if reasonW != nil {
		rr.w = reasonW
	} else if onToken != nil {
		rr.onToken = onToken
	} else if reasoning {
		rr.w = os.Stdout
	}
	return rr
}

func (r *reasoningRouter) writePrefix() {
	w := r.effectiveWriter()
	if w == nil {
		return
	}
	fmt.Fprintf(w, "\n%s ", ui.Styles["Reasoning"].Render("[PAI \U0001f914]"))
}

func (r *reasoningRouter) writeToken(tok string) {
	if r.onToken != nil && r.w == nil {
		r.onToken(tok)
	} else if r.w != nil {
		fmt.Fprint(r.w, ui.Styles["Thinking"].Render(tok))
	}
}

func (r *reasoningRouter) writeSeparator() {
	w := r.effectiveWriter()
	if w != nil {
		fmt.Fprintf(w, "\n%s\n", ui.Styles["Separator"].Render(strings.Repeat("\u2500", 40)))
	}
}

// effectiveWriter returns the io.Writer that should receive separator/pre/suffix.
func (r *reasoningRouter) effectiveWriter() io.Writer {
	if r.w != nil {
		return r.w
	}
	if r.fallback {
		return os.Stdout
	}
	return nil
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

// extractJSON extracts the first JSON object from a string.
func extractJSON(content string) (string, error) {
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
	return s[:max] + fmt.Sprintf("\n\u2026 [truncated %d bytes]", len(s)-max)
}
