package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

// Ports are the host capabilities chat needs. chat deliberately does not depend
// on any presentation or storage package — only on these ports.
type Ports struct {
	Provider provider.Provider
	Observer core.Observer
	Logger   core.Logger
}

// ---------------------------------------------------------------------------
// Core chat
// ---------------------------------------------------------------------------

// chat sends messages to the LLM and returns the assistant's content as a JSON
// string plus token usage. It always requests JSON mode; the caller parses with
// ParseResponse.
//
// history holds only conversation turns; the role prompt is composed around it
// at send time (see RolePrompt.Messages).
func chat(
	ctx context.Context,
	cfg *config.UserConfig,
	rp *RolePrompt,
	p Ports,
	history []provider.Message,
	stream bool,
) (content string, newHistory []provider.Message, usage *provider.Usage, err error) {

	params := provider.CompletionParams{
		Model:           cfg.Model,
		Messages:        rp.Messages(history),
		Stream:          stream,
		ResponseFormat:  &provider.ResponseFormat{Type: "json_object"},
		ReasoningEffort: cfg.ReasoningEffort,
	}

	showReasoning := cfg.ReasoningEffort != provider.ReasoningEffortNone
	if stream {
		content, usage, err = doStream(ctx, p, params, showReasoning)
	} else {
		var resp *provider.ChatCompletion
		resp, err = p.Provider.Completion(ctx, params)
		if err == nil && resp != nil {
			content, usage = extractCompletion(resp, showReasoning, p.Observer)
		}
	}
	if err != nil {
		return "", nil, nil, err
	}

	newHistory = append(history, provider.Message{Role: provider.RoleAssistant, Content: content})
	return content, newHistory, usage, nil
}

// extractCompletion unpacks a non-streaming response, streaming any reasoning
// content to the observer, and returns content + usage.
func extractCompletion(resp *provider.ChatCompletion, showReasoning bool, obs core.Observer) (string, *provider.Usage) {
	if len(resp.Choices) == 0 {
		return "", resp.Usage
	}
	choice := resp.Choices[0]
	if showReasoning && choice.Reasoning != nil && choice.Reasoning.Content != "" {
		obs.Reasoning(choice.Reasoning.Content)
	}
	return choice.Message.Content, resp.Usage
}

// ChatStr sends the conversation and returns the assistant's raw JSON string.
func ChatStr(ctx context.Context, cfg *config.UserConfig, rp *RolePrompt, p Ports, history []provider.Message) (string, []provider.Message, *provider.Usage, error) {
	return chat(ctx, cfg, rp, p, history, cfg.Streaming)
}

// ---------------------------------------------------------------------------
// Streaming implementation
// ---------------------------------------------------------------------------

// doStream reads streaming chunks, forwards reasoning tokens to the observer,
// collects content tokens, and tracks usage.
func doStream(
	ctx context.Context,
	p Ports,
	params provider.CompletionParams,
	showReasoning bool,
) (string, *provider.Usage, error) {
	chunkChan, errChan := p.Provider.CompletionStream(ctx, params)

	var fullContent strings.Builder
	var usage *provider.Usage

	for chunk := range chunkChan {
		// Usage typically arrives in the final chunk.
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if showReasoning && choice.Delta.Reasoning != nil && choice.Delta.Reasoning.Content != "" {
				p.Observer.Reasoning(choice.Delta.Reasoning.Content)
			}
			if token := choice.Delta.Content; token != "" {
				fullContent.WriteString(token)
			}
		}
	}

	if err := <-errChan; err != nil {
		return "", nil, err
	}
	return fullContent.String(), usage, nil
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
