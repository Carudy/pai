package chat

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

// elisionMarker prefixes every notice that content was dropped, so the model can
// tell an omission from real output and re-run the command if it needs to.
const elisionMarker = "\u2026"

// Truncate shortens one tool observation before it is fed back to the model.
//
// It is line-oriented rather than a plain byte cut: the tail matters as much as
// the head, because errors, exit summaries and totals land at the end of command
// output, and a head-only cut hides exactly the lines the model usually needs.
// MaxBytes is a backstop for output with very long lines.
type Truncate struct {
	MaxBytes  int
	HeadLines int
	TailLines int
}

// Apply returns s shortened to the budget, inserting a visible marker where
// content was dropped. Output that already fits is returned untouched.
func (t Truncate) Apply(s string) string {
	if s == "" {
		return s
	}

	head := max(t.HeadLines, 0)
	tail := max(t.TailLines, 0)

	lines := strings.Split(s, "\n")
	if (head == 0 && tail == 0) || len(lines) <= head+tail {
		return t.byteCap(s)
	}

	kept := make([]string, 0, head+tail+1)
	kept = append(kept, lines[:head]...)
	kept = append(kept, fmt.Sprintf("%s [%d lines elided]", elisionMarker, len(lines)-head-tail))
	kept = append(kept, lines[len(lines)-tail:]...)
	return t.byteCap(strings.Join(kept, "\n"))
}

// byteCap is the last-resort cut for a single very long line.
func (t Truncate) byteCap(s string) string {
	if t.MaxBytes <= 0 || len(s) <= t.MaxBytes {
		return s
	}
	return cutBytes(s, t.MaxBytes) + fmt.Sprintf("\n%s [truncated %d bytes]", elisionMarker, len(s)-t.MaxBytes)
}

// cutBytes returns the longest prefix of s that fits in max bytes without
// splitting a UTF-8 rune.
func cutBytes(s string, max int) string {
	if max >= len(s) {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// CompactHistory returns a copy of history in which old tool observations are
// elided down to their header, so a long session does not resend stale command
// output on every turn. The most recent KeepTurns messages are replayed
// verbatim, and nothing is elided until the conversation exceeds ElideAfterTurns
// messages — a short session is never rewritten.
//
// Only tool results are candidates. User input, assistant replies and user
// answers carry the task and its decisions, so they are never dropped. The full
// text stays in the session store regardless; only the model's replay is
// compressed, so attaching later still shows everything.
func CompactHistory(history []provider.Message, c config.ContextConfig) []provider.Message {
	if len(history) == 0 {
		return history
	}
	if c.ElideAfterTurns > 0 && len(history) <= c.ElideAfterTurns {
		return history
	}

	cutoff := len(history) - max(c.KeepTurns, 0)
	if cutoff <= 0 {
		return history
	}

	out := make([]provider.Message, len(history))
	copy(out, history)
	for i := 0; i < cutoff; i++ {
		m := &out[i]
		if m.Kind != core.KindToolResult || len(m.Content) <= c.ElideMinBytes {
			continue
		}
		m.Content = elideObservation(m.Content, c.ElideHeadLines)
	}
	return out
}

// elideObservation keeps the first headLines of a tool observation — its label,
// command and exit status — and replaces the body with a marker. The header is
// what the model needs to decide whether to re-run the command.
func elideObservation(content string, headLines int) string {
	if headLines <= 0 {
		headLines = 8
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= headLines {
		return content
	}
	return strings.Join(lines[:headLines], "\n") +
		fmt.Sprintf("\n%s [%d lines of earlier output elided; re-run the command if you need them]",
			elisionMarker, len(lines)-headLines)
}
