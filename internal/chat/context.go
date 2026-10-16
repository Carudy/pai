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

// A turn is one user instruction plus everything the model does to answer it:
// the assistant replies and tool observations that follow, up to the next user
// instruction. Assistant replies and tool results both ride the user role on the
// wire, so a turn is recognised by its kind, not by its role.
func isTurnStart(m provider.Message) bool {
	return m.Kind == core.KindInput || m.Kind == core.KindUserAnswer
}

// userTurns counts the user turns in history.
func userTurns(history []provider.Message) int {
	n := 0
	for _, m := range history {
		if isTurnStart(m) {
			n++
		}
	}
	return n
}

// keepFrom returns the index where the oldest turn to keep verbatim begins: the
// start of the turns-th user turn from the end. The whole turn is kept, however
// many tool calls it took, so the model never loses the observations of the task
// it is in the middle of. When history has fewer than turns user turns it returns
// 0 (nothing is old enough), and with turns <= 0 it returns len(history) (the
// verbatim window is empty).
func keepFrom(history []provider.Message, turns int) int {
	if turns <= 0 {
		return len(history)
	}
	seen := 0
	for i := len(history) - 1; i >= 0; i-- {
		if !isTurnStart(history[i]) {
			continue
		}
		seen++
		if seen >= turns {
			return i
		}
	}
	return 0
}

// CompactHistory returns a copy of history in which old tool observations are
// elided down to their header, so a long session does not resend stale command
// output on every request. The most recent KeepTurns user turns are replayed
// verbatim — a whole turn, so the model keeps every observation of the task it is
// currently on — and nothing is elided until the conversation exceeds
// ElideAfterTurns user turns, so a short session is never rewritten.
//
// Only tool results are candidates. User input, assistant replies and user
// answers carry the task and its decisions, so they are never dropped. The full
// text stays in the session store regardless; only the model's replay is
// compressed, so attaching later still shows everything.
func CompactHistory(history []provider.Message, c config.ContextConfig) []provider.Message {
	out, _ := compact(history, c)
	return out
}

// CompactionInfo reports what deterministic elision did to a history: how many
// old tool observations were replaced with a header and how many lines that
// dropped. The zero value means nothing was elided.
type CompactionInfo struct {
	Observations int
	Lines        int
	KeptTurns    int
}

// DescribeCompaction renders the deterministic elision CompactHistory would apply
// to history as a short one-line notice, or "" when nothing is elided. The role
// loop logs it when its footprint changes, so a reader can see why older turns
// stop being resent every request.
func DescribeCompaction(history []provider.Message, c config.ContextConfig) string {
	if _, info := compact(history, c); info.Observations > 0 {
		return fmt.Sprintf("context: elided %d older tool observation(s), dropping %d line(s); last %d turn(s) kept verbatim",
			info.Observations, info.Lines, info.KeptTurns)
	}
	return ""
}

// compact is CompactHistory plus a report of what it did; it returns the input
// slice unchanged (and a zero report) whenever nothing is old enough to elide.
func compact(history []provider.Message, c config.ContextConfig) ([]provider.Message, CompactionInfo) {
	if len(history) == 0 {
		return history, CompactionInfo{}
	}
	if c.ElideAfterTurns > 0 && userTurns(history) <= c.ElideAfterTurns {
		return history, CompactionInfo{}
	}

	cutoff := keepFrom(history, c.KeepTurns)
	if cutoff <= 0 {
		return history, CompactionInfo{}
	}

	info := CompactionInfo{KeptTurns: c.KeepTurns}
	out := make([]provider.Message, len(history))
	copy(out, history)
	for i := 0; i < cutoff; i++ {
		m := &out[i]
		if m.Kind != core.KindToolResult || len(m.Content) <= c.ElideMinBytes {
			continue
		}
		elided, dropped := elideObservation(m.Content, c.ElideHeadLines)
		if dropped <= 0 {
			continue
		}
		m.Content = elided
		info.Observations++
		info.Lines += dropped
	}
	return out, info
}

// elideObservation keeps the first headLines of a tool observation — its label,
// command and exit status — and replaces the body with a marker, returning the
// number of lines dropped. The header is what the model needs to decide whether
// to re-run the command.
func elideObservation(content string, headLines int) (string, int) {
	if headLines <= 0 {
		headLines = 8
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= headLines {
		return content, 0
	}
	dropped := len(lines) - headLines
	return strings.Join(lines[:headLines], "\n") +
		fmt.Sprintf("\n%s [%d lines of earlier output elided; re-run the command if you need them]",
			elisionMarker, dropped), dropped
}
