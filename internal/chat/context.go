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
	return isTurnStartKind(m.Kind)
}

// isTurnStartKind reports whether a stored turn begins a user turn. The message
// and turn domains share one definition, so the replay window compaction kept and
// the window a later replay reconstructs cannot drift apart.
func isTurnStartKind(kind string) bool {
	return kind == core.KindInput || kind == core.KindUserAnswer
}

// UserTurns counts the user turns in a message list. A turn is one user
// instruction plus everything the model did to answer it, so this is the unit
// keep_turns and the compaction layers count.
func UserTurns(history []provider.Message) int {
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

// ReplayTurns selects the turns to replay into the model: the last summary
// checkpoint stands in for the turns before it, so they are neither resent nor
// re-summarized, though they stay on disk. Two details keep that replay faithful
// to the history a running loop already holds.
//
// A checkpoint is appended at the end of the store, which lands it *after* the
// keepTurns user turns compaction kept verbatim and deliberately did not
// summarize. Those turns are replayed ahead of the checkpoint's tail; without them
// a fresh runtime would silently lose the most recent turns, which the summary
// does not cover. maxTurns then caps the tail after the checkpoint, never dropping
// the summary or that verbatim window.
//
// Callers on every frontend share this, so what the CLI replays and what a served
// runtime replays cannot drift apart.
func ReplayTurns(turns []core.Turn, keepTurns, maxTurns int) []core.Turn {
	start := lastSummaryIndex(turns)
	if start < 0 {
		if maxTurns > 0 && len(turns) > maxTurns {
			return turns[len(turns)-maxTurns:]
		}
		return turns
	}

	summary := turns[start]
	window := keepWindow(turns[:start], keepTurns)
	rest := turns[start+1:]
	if maxTurns > 0 && len(rest) > maxTurns {
		rest = rest[len(rest)-maxTurns:]
	}

	out := make([]core.Turn, 0, len(window)+len(rest)+1)
	out = append(out, summary)
	out = append(out, window...)
	return append(out, rest...)
}

// keepWindow returns the tail of turns beginning at the keepTurns-th user turn
// counted back from the end: exactly the window CompactHistory left verbatim when
// it wrote the checkpoint that follows it. Fewer turns than keepTurns, or
// keepTurns <= 0, means there is no window to reconstruct.
func keepWindow(turns []core.Turn, keepTurns int) []core.Turn {
	if keepTurns <= 0 {
		return nil
	}
	seen := 0
	for i := len(turns) - 1; i >= 0; i-- {
		if !isTurnStartKind(turns[i].Kind) {
			continue
		}
		seen++
		if seen >= keepTurns {
			return turns[i:]
		}
	}
	return nil
}

// lastSummaryIndex returns the index of the most recent summary checkpoint, or -1.
func lastSummaryIndex(turns []core.Turn) int {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Kind == core.KindSummary {
			return i
		}
	}
	return -1
}

// ContextCard is one turn's place in the context-to-send: the stored Original,
// what the model would actually receive (Sent, empty when nothing is sent), and
// why they differ (State).
type ContextCard struct {
	Seq      int
	Role     string
	Kind     string
	Original string
	Sent     string
	State    string // see State* below
}

// Context-card states. Everything except sent/elided/edited is *not* sent.
const (
	StateSent           = "sent"            // sent verbatim
	StateElided         = "elided"          // sent with old tool output trimmed
	StateEdited         = "edited"          // a user override replaced the text
	StateDropped        = "dropped"         // a user override removed it
	StateSummarizedAway = "summarized_away" // stands before the last summary checkpoint
	StateCapped         = "capped"          // past session.max_turns
)

// Override is a user's surgery on one stored turn, keyed by Turn.Seq.
type Override struct {
	Action  string // core.OverrideReplace | core.OverrideDrop
	Content string // replacement text for OverrideReplace
}

// ApplyOverrides returns turns with user overrides applied: a replace swaps the
// content, a drop removes the turn. Turns are matched by Seq, so order and
// identity are preserved for everything that survives.
func ApplyOverrides(turns []core.Turn, overrides map[int]Override) []core.Turn {
	if len(overrides) == 0 {
		return turns
	}
	out := make([]core.Turn, 0, len(turns))
	for _, t := range turns {
		o, ok := overrides[t.Seq]
		if ok && o.Action == core.OverrideDrop {
			continue
		}
		if ok && o.Action == core.OverrideReplace {
			t.Content = o.Content
		}
		out = append(out, t)
	}
	return out
}

// ContextCards reports, for every stored turn, what the next request would send
// and why. It is the read-only half of the context inspector: the same selection
// (ReplayTurns) and the same trimming (CompactHistory) the loop applies, reported
// per turn instead of as one message list. The system head and tail are not
// included — the role prompt composes those.
//
// A turn that is not in the replay window is summarized_away (it precedes the
// last summary checkpoint) or capped (past max_turns); an override on such a turn
// changes nothing about the send, so the card reports what is actually sent.
func ContextCards(turns []core.Turn, overrides map[int]Override, cfg config.ContextConfig, maxTurns int) []ContextCard {
	lastSummary := lastSummaryIndex(turns)
	summarySeq := -1
	if lastSummary >= 0 {
		summarySeq = turns[lastSummary].Seq
	}

	replayed := ReplayTurns(turns, cfg.KeepTurns, maxTurns)
	inReplay := make(map[int]bool, len(replayed))
	for _, t := range replayed {
		inReplay[t.Seq] = true
	}

	// Compute what is actually sent by running the same pipeline the loop does:
	// overrides, then deterministic elision. Index parity between overridden and
	// compacted holds because CompactHistory preserves length and order.
	overridden := ApplyOverrides(replayed, overrides)
	compacted := CompactHistory(messagesFromTurns(overridden), cfg)
	sent := make(map[int]string, len(overridden))
	for i, t := range overridden {
		sent[t.Seq] = compacted[i].Content
	}

	cards := make([]ContextCard, len(turns))
	for i, t := range turns {
		c := ContextCard{Seq: t.Seq, Role: t.Role, Kind: t.Kind, Original: t.Content}
		o, hasOverride := overrides[t.Seq]
		switch {
		case !inReplay[t.Seq]:
			if summarySeq >= 0 && t.Seq < summarySeq {
				c.State = StateSummarizedAway
			} else {
				c.State = StateCapped
			}
		case hasOverride && o.Action == core.OverrideDrop:
			c.State = StateDropped
		case hasOverride && o.Action == core.OverrideReplace:
			c.Sent = sent[t.Seq]
			c.State = StateEdited
		default:
			c.Sent = sent[t.Seq]
			if c.Sent != c.Original {
				c.State = StateElided
			} else {
				c.State = StateSent
			}
		}
		cards[i] = c
	}
	return cards
}

// messagesFromTurns maps turns to the wire messages the provider sees.
func messagesFromTurns(turns []core.Turn) []provider.Message {
	msgs := make([]provider.Message, 0, len(turns))
	for _, t := range turns {
		msgs = append(msgs, provider.Message{Role: t.Role, Content: t.Content, Kind: t.Kind})
	}
	return msgs
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

// DescribeNoCompaction explains why a compaction has nothing to do, so a manual
// /compact can say why instead of looking like a button that did nothing. It
// returns "" when there is something to summarize.
func DescribeNoCompaction(history []provider.Message, keepTurns int) string {
	span, recent := SplitForSummary(history, keepTurns)
	if UserTurns(span) > 0 {
		return ""
	}
	if len(span) > 0 {
		return fmt.Sprintf("nothing to compact: no user turn older than keep_turns = %d; re-summarizing the existing summary would only lose detail", keepTurns)
	}
	return fmt.Sprintf("nothing to compact: all %d user turn(s) still fit inside the keep_turns = %d window", UserTurns(recent), keepTurns)
}

// compact is CompactHistory plus a report of what it did; it returns the input
// slice unchanged (and a zero report) whenever nothing is old enough to elide.
func compact(history []provider.Message, c config.ContextConfig) ([]provider.Message, CompactionInfo) {
	if len(history) == 0 {
		return history, CompactionInfo{}
	}
	if c.ElideAfterTurns > 0 && UserTurns(history) <= c.ElideAfterTurns {
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
