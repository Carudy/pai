package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"pai/internal/config"
)

// chatStreamWriter performs streaming completion and writes each token to w.
// If w is nil, tokens are silently collected without printing to the terminal.
// Returns the full content and updated history, following the same contract as chat().
func chatStreamWriter(ctx context.Context, cfg *config.UserConfig,
	provider *anyllm.Provider, history []anyllm.Message, w io.Writer) (string, []anyllm.Message, error) {

	chunkChan, errChan := (*provider).CompletionStream(ctx, anyllm.CompletionParams{
		Model:    cfg.Model,
		Messages: history,
		Stream:   true,
	})

	var fullContent strings.Builder

	for chunk := range chunkChan {
		for _, choice := range chunk.Choices {
			token := choice.Delta.Content
			fullContent.WriteString(token)
			if w != nil {
				fmt.Fprint(w, token)
			}
		}
	}

	// Trailing newline if we were streaming to the terminal.
	if w != nil {
		fmt.Fprintln(w)
	}

	// Drain the error channel.
	if err := <-errChan; err != nil {
		return "", nil, err
	}

	content := fullContent.String()
	newHistory := append(history, anyllm.Message{Role: anyllm.RoleAssistant, Content: content})
	return content, newHistory, nil
}

// chat performs a chat completion. When cfg.Streaming is true and the
// provider supports it, tokens are collected silently without printing
// (the caller manages its own display, e.g. the TUI chat).
// Otherwise it uses the blocking Completion call.
//
// Returns the full response content and the updated message history.
func chat(ctx context.Context, cfg *config.UserConfig,
	provider *anyllm.Provider, history []anyllm.Message) (string, []anyllm.Message, error) {

	if cfg.Streaming {
		return chatStreamWriter(ctx, cfg, provider, history, nil)
	}

	resp, err := (*provider).Completion(ctx, anyllm.CompletionParams{
		Model:    cfg.Model,
		Messages: history,
	})
	if err != nil {
		return "", nil, err
	}
	if len(resp.Choices) == 0 {
		return "", nil, fmt.Errorf("no choices in response")
	}
	content, ok := resp.Choices[0].Message.Content.(string)
	if !ok {
		return "", nil, fmt.Errorf("unexpected content type: %T", resp.Choices[0].Message.Content)
	}

	newHistory := append(history, anyllm.Message{Role: anyllm.RoleAssistant, Content: content})
	return content, newHistory, nil
}

// chatStdout is a convenience wrapper around chat that streams tokens to
// os.Stdout in real time when cfg.Streaming is true. Use this for agents
// that render their own structured output *after* the LLM response (cmd,
// devops, QA single-turn). Use chat() directly when the caller manages
// its own display (QA multi-turn TUI).
func chatStdout(ctx context.Context, cfg *config.UserConfig,
	provider *anyllm.Provider, history []anyllm.Message) (string, []anyllm.Message, error) {

	if cfg.Streaming {
		return chatStreamWriter(ctx, cfg, provider, history, os.Stdout)
	}
	return chat(ctx, cfg, provider, history)
}

// ExtractJSON finds the first '{' and last '}' in content and returns the
// substring between them. This is used to extract a JSON object from an
// LLM response that may include surrounding commentary.
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
