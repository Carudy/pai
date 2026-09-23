package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

// This file is the second compression layer: when the conversation outgrows a
// token budget, the oldest span is summarized by the model into a single message
// and the recent turns are kept verbatim. It is separate from CompactHistory
// (context.go), which deterministically elides old observations and always runs.

// ShouldSummarize reports whether the last prompt exceeded the budget. A zero
// budget disables summarization, so it is opt-in (it costs an extra model call).
func ShouldSummarize(c config.ContextConfig, promptTokens int) bool {
	return c.SummarizeAfterTokens > 0 && promptTokens >= c.SummarizeAfterTokens
}

// SplitForSummary divides history into the span to summarize and the recent
// messages to keep. An empty span means there is nothing old enough.
func SplitForSummary(history []provider.Message, keep int) (span, recent []provider.Message) {
	if keep < 0 {
		keep = 0
	}
	if len(history) <= keep {
		return nil, history
	}
	cut := len(history) - keep
	return history[:cut], history[cut:]
}

// EstimateTokens is a rough size for a message list, used only to decide whether
// to summarize before the model has reported real usage (e.g. on resume). Roughly
// four bytes per token; it deliberately overestimates, erring toward compacting.
func EstimateTokens(msgs []provider.Message) int {
	total := 0
	for _, m := range msgs {
		total += len(m.Content)
	}
	return total / 4
}

// SummaryMessage wraps a summary as a system message so it reads as context
// rather than as something the user or the model said.
func SummaryMessage(summary string) provider.Message {
	return provider.Message{
		Role:    provider.RoleSystem,
		Kind:    core.KindSummary,
		Content: "[conversation summary so far]\n" + strings.TrimSpace(summary),
	}
}

// summarySystemPrompt is the instruction for the summarizer call. It asks for the
// facts a later turn needs and nothing else.
const summarySystemPrompt = `You compress an agent's conversation history so the work can continue in a
smaller context window. Write a summary that lets a later turn pick up without
the original messages. Keep, in this order:
- the user's goal and any constraints they stated
- decisions made, and why
- concrete state: files and paths touched, commands run and their key results,
  hostnames, versions, and error messages (keep these verbatim)
- what is still open or unverified
Drop pleasantries and repeated output. Be specific and terse; use short bullets.
Return plain text only — no JSON, no code fences, no preamble.`

// Summarize asks the model to compress a transcript. It deliberately does not
// stream, does not request reasoning, and does not ask for JSON: this is a
// mechanical task where that machinery only adds cost and latency.
func Summarize(ctx context.Context, cfg *config.UserConfig, p Ports, transcript string) (string, error) {
	params := provider.CompletionParams{
		Model: cfg.Model,
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: summarySystemPrompt},
			{Role: provider.RoleUser, Content: transcript},
		},
		ReasoningEffort: provider.ReasoningEffortNone,
	}

	resp, err := p.Provider.Completion(ctx, params)
	if err != nil {
		return "", fmt.Errorf("summarize: %w", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		return "", fmt.Errorf("summarize: empty response")
	}
	out := strings.TrimSpace(resp.Choices[0].Message.Content)
	if out == "" {
		return "", fmt.Errorf("summarize: empty summary")
	}
	return out, nil
}

// RenderTranscript flattens messages into a labelled transcript for the
// summarizer, so it can tell user requests, model replies and tool results apart.
func RenderTranscript(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(messageLabel(m))
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func messageLabel(m provider.Message) string {
	switch m.Kind {
	case core.KindToolResult:
		return "TOOL RESULT"
	case core.KindUserAnswer:
		return "USER ANSWER"
	case core.KindOutput:
		return "ASSISTANT"
	case core.KindSummary:
		return "PRIOR SUMMARY"
	case core.KindInput:
		return "USER"
	}
	if m.Role == provider.RoleAssistant {
		return "ASSISTANT"
	}
	return "USER"
}
