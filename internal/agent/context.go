package agent

import "github.com/Carudy/pai/internal/llm"

const maxHistoryBytes = 32_0000

func trimHistory(history []llm.Message) []llm.Message {
	total := 0
	for _, m := range history {
		total += len(m.Content)
	}
	if total <= maxHistoryBytes {
		return history
	}

	// Always keep index 0 (system prompt).
	// Walk from the end forward to find how many fit.
	keep := 1 // system
	accum := len(history[0].Content)
	for i := len(history) - 1; i > 0 && accum < maxHistoryBytes; i-- {
		accum += len(history[i].Content)
		keep++
	}
	start := len(history) - (keep - 1)
	if start < 1 {
		start = 1
	}
	trimmed := make([]llm.Message, 0, 1+keep)
	trimmed = append(trimmed, history[0])
	trimmed = append(trimmed, history[start:]...)
	return trimmed
}
