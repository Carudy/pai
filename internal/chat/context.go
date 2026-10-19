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

// Context-card states. Everything except sent/elided/trimmed/edited is *not* sent.
const (
	StateSent           = "sent"            // sent verbatim
	StateElided         = "elided"          // sent with old tool output trimmed
	StateTrimmed        = "trimmed"         // sent with the model's over-run cut off
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
// (ReplayTurns) and the same trimming (ServedHistory) the loop applies, reported
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
	// overrides, then the over-run cut, then deterministic elision. Index parity
	// between the stages holds because both preserve length and order.
	overridden := ApplyOverrides(replayed, overrides)
	trimmed, compacted := servedStages(messagesFromTurns(overridden), cfg)
	sent := make(map[int]string, len(overridden))
	cut := make(map[int]bool, len(overridden))
	for i, t := range overridden {
		sent[t.Seq] = compacted[i].Content
		cut[t.Seq] = trimmed[i].Content != t.Content
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
			switch {
			case c.Sent == c.Original:
				c.State = StateSent
			case cut[t.Seq]:
				c.State = StateTrimmed
			default:
				c.State = StateElided
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
// elided to a head and a tail, so a long session does not resend stale command
// output on every request. The most recent KeepTurns user turns are replayed
// verbatim — a whole turn, so the model keeps every observation of the task it is
// currently on — and nothing is elided until the history itself reaches the
// ElideAfterTokens budget, so a short session is never rewritten.
//
// The trigger is size, not age. A prompt cache is an exact prefix, so eliding one old
// observation — the oldest sits near the front of the history — invalidates every
// token after it: the whole recent tail, which is the expensive part to re-read.
// Gating on the context size keeps a small session from being rewritten at all, when
// the edit would land almost at the front and buy a couple of kilobytes at the price
// of re-reading the whole request.
//
// Above the budget the front still has to move as turns leave the window, so it moves
// in whole batches (see elideFront): a turn that crosses the boundary alone does not
// rewrite the prompt. The price of batching is that up to one batch of old output is
// replayed verbatim, which under a warm cache costs a tenth of its size per request —
// cheaper than the one full re-read a per-turn edit would cost.
//
// Only tool results are candidates. User input, assistant replies and user
// answers carry the task and its decisions, so they are never dropped. The full
// text stays in the session store regardless; only the model's replay is
// compressed, so attaching later still shows everything.
func CompactHistory(history []provider.Message, c config.ContextConfig) []provider.Message {
	out, _ := compact(history, c)
	return out
}

// ServedHistory applies every replay-time edit to the messages a session would send:
// the model's over-run is cut back to the action it took, then old observations are
// elided. Both are pure functions of the list and the config, so the loop, a resumed
// runtime and the inspector cannot disagree about what a request contains.
func ServedHistory(history []provider.Message, c config.ContextConfig) []provider.Message {
	_, sent := servedStages(history, c)
	return sent
}

// servedStages is ServedHistory with the intermediate result exposed: the list the
// over-run was cut from, and the one that is actually sent. The inspector needs the
// first to say which cards were cut rather than elided.
func servedStages(history []provider.Message, c config.ContextConfig) (trimmed, sent []provider.Message) {
	trimmed = TrimHistory(history)
	return trimmed, CompactHistory(trimmed, c)
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

// elideBatchFraction sizes one elision batch as a share of the trigger: with the
// default 256k trigger a batch is 32k tokens. It bounds the price of batching — the
// output still replayed verbatim — at an eighth of the elision budget, and it collapses
// to per-turn elision when the trigger is tiny, which is what a small budget wants.
const elideBatchFraction = 8

// elideFront returns the index where the verbatim replay ends and elision begins: the
// start of the keep window, pulled back to the last whole batch of raw history.
//
// Anchoring the steps to the start of the replayed list — rather than to the previous
// sweep, which this function cannot see — keeps it a pure function of the history: the
// same input always yields the same front, a longer history never moves it backwards,
// and it advances only when another batch has accumulated. A batch of zero means no
// batching, so the front follows the window turn by turn.
func elideFront(history []provider.Message, keepTurns, batch int) int {
	cut := keepFrom(history, keepTurns)
	if cut <= 0 || batch <= 0 {
		return cut
	}
	whole := (EstimateTokens(history[:cut]) / batch) * batch
	if whole == 0 {
		return 0 // nothing has accumulated a whole batch yet
	}
	// The front is the first turn that straddles the boundary; only tool results before
	// it are elided, so landing mid-turn costs nothing.
	total := 0
	for i := 0; i < cut; i++ {
		total += estimateText(history[i].Content)
		if total > whole {
			return i
		}
	}
	return cut
}

// compact is CompactHistory plus a report of what it did; it returns the input
// slice unchanged (and a zero report) whenever nothing is old enough to elide.
func compact(history []provider.Message, c config.ContextConfig) ([]provider.Message, CompactionInfo) {
	if len(history) == 0 {
		return history, CompactionInfo{}
	}
	// Size, not age: see CompactHistory. Measuring the raw history keeps this a pure
	// function of its inputs, and it only grows, so the decision cannot oscillate.
	limit := c.ElideLimit()
	if limit <= 0 || EstimateTokens(history) < limit {
		return history, CompactionInfo{}
	}

	cutoff := elideFront(history, c.KeepTurns, limit/elideBatchFraction)
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
		elided, dropped := elideObservation(m.Content, c.ElideHeadLines, c.ElideTailLines)
		if dropped <= 0 {
			continue
		}
		m.Content = elided
		info.Observations++
		info.Lines += dropped
	}
	return out, info
}

// elideObservation keeps the first headLines and the last tailLines of a tool
// observation and replaces what lay between them with a marker, returning the number
// of lines dropped. It is deliberately the same shape as the capture-time truncation,
// and for the same reason: the head names the command and its exit status, while
// errors and summaries land at the end of output, so a tail is often the part the
// model needs to decide whether to re-run the command.
func elideObservation(content string, headLines, tailLines int) (string, int) {
	if headLines <= 0 {
		headLines = 8
	}
	tailLines = max(tailLines, 0)
	lines := strings.Split(content, "\n")
	if len(lines) <= headLines+tailLines {
		return content, 0
	}
	dropped := len(lines) - headLines - tailLines
	kept := make([]string, 0, headLines+tailLines+1)
	kept = append(kept, lines[:headLines]...)
	kept = append(kept, fmt.Sprintf("%s [%d lines of earlier output elided; re-run the command if you need them]",
		elisionMarker, dropped))
	kept = append(kept, lines[len(lines)-tailLines:]...)
	return strings.Join(kept, "\n"), dropped
}
