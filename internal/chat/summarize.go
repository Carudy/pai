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
// messages to keep, keeping the last keepTurns user turns verbatim (a whole turn,
// not a message count, so a long single turn is not truncated mid-task). An empty
// span means there is nothing old enough.
func SplitForSummary(history []provider.Message, keepTurns int) (span, recent []provider.Message) {
	cut := keepFrom(history, keepTurns)
	if cut <= 0 {
		return nil, history
	}
	return history[:cut], history[cut:]
}

// EstimateTokens is a rough size for a message list, used only when the provider
// has not reported real usage (on resume, or a stream without usage). Four bytes
// per token is a coarse approximation: close for prose, but it can undercount code
// or non-Latin text, so callers pass the composed prompt (system head and tail
// included) rather than the bare history.
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

// summarySystemPrompt is the instruction for the summarizer call. The span it
// receives is the older part of a conversation whose most recent turns are kept
// verbatim, so the result is background for a continuing conversation rather than
// a statement of the latest state — and it is weighted by recency, so an old task
// is never presented as the current one. It deliberately does not ask for the
// environment facts the role prompt already carries (OS, shell, user, date,
// working directory): repeating them would spend tokens and could contradict a
// session whose working directory changed since.
const summarySystemPrompt = `You compress the older part of an agent's conversation so the work can continue
without those messages. The most recent exchanges are kept verbatim elsewhere and
are not part of your input, so what you write is background, not the latest state.

The span is in chronological order, and its end matters far more than its start.
Weight it that way:
- The task being worked on at the end of the span is the current one: describe it
  fully, including exactly where it stopped.
- Tasks finished earlier are history: usually one line each, and only the parts
  that later work still depends on.
- If the work changed direction, or a goal was dropped or replaced, say so plainly.
  Never present a finished or abandoned goal as the current one.

Answer with these labels, in this order:
CURRENT: the task in flight at the end of the span, and its state.
OPEN: what is unfinished, unverified, or waiting on the user.
STATE: identifiers a later turn cannot guess — file paths, remote hosts, hostnames,
  versions, command lines, error messages. Keep them verbatim; do not paraphrase.
  The reader is already given this machine's OS, shell, user, date and working
  directory elsewhere, so do not restate those — and say nothing about the session
  environment that could go stale.
CONSTRAINTS: what the user required, in their own terms.
EARLIER: one line per finished task, most recent first; omit any that no longer
  matter.

Write in the language of the most recent user turns, even if the earlier span is in
another language, and keep identifiers exactly as they appear whatever their
language.
Be terse: short bullets, no nesting, no pleasantries, no repeated command output.
Report only what the span says — never invent details. Stay well under the size of
the text you were given. Plain text only: no JSON, no code fences, no preamble.`

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
