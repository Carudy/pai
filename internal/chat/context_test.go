package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

// The tail is kept on purpose: errors and summaries land at the end of command
// output, so a head-only cut would hide exactly the useful lines.
func TestTruncateKeepsHeadAndTail(t *testing.T) {
	lines := make([]string, 0, 100)
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	s := strings.Join(lines, "\n")

	got := (Truncate{MaxBytes: 1 << 20, HeadLines: 3, TailLines: 2}).Apply(s)
	for _, want := range []string{"line 1", "line 3", "line 99", "line 100"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "line 50") {
		t.Errorf("middle should be elided:\n%s", got)
	}
	if !strings.Contains(got, "elided") {
		t.Errorf("elision marker missing:\n%s", got)
	}
}

func TestTruncatePassesThroughWhenItFits(t *testing.T) {
	s := "a\nb\nc"
	if got := (Truncate{MaxBytes: 1000, HeadLines: 80, TailLines: 40}).Apply(s); got != s {
		t.Errorf("small output changed: %q", got)
	}
}

// A single very long line still has to be bounded by bytes.
func TestTruncateByteCap(t *testing.T) {
	got := (Truncate{MaxBytes: 100}).Apply(strings.Repeat("x", 5000))
	if len(got) > 200 {
		t.Errorf("not byte-capped: %d bytes", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("byte-cap marker missing: %q", got)
	}
}

func TestTruncateByteCapKeepsRunesWhole(t *testing.T) {
	got := (Truncate{MaxBytes: 4}).Apply(strings.Repeat("é", 10)) // 2 bytes per rune
	if !strings.HasPrefix(got, "éé") {
		t.Errorf("split a rune: %q", got)
	}
}

func toolResult(content string) provider.Message {
	return provider.Message{Role: provider.RoleUser, Kind: core.KindToolResult, Content: content}
}

// Turns, not messages, decide the verbatim window: an old turn's large
// observation is elided, but the whole current turn is kept however many tool
// calls it took, so the model never loses the task it is in the middle of.
func TestCompactHistoryElidesOnlyOldTurns(t *testing.T) {
	c := config.ContextConfig{KeepTurns: 1, ElideAfterTurns: 0, ElideMinBytes: 10, ElideHeadLines: 3}
	big := "[cmd result]\nCOMMAND: df -h\nEXIT_ERROR: <nil>\nOUTPUT:\n" + strings.Repeat("row\n", 100)

	history := []provider.Message{
		{Role: provider.RoleUser, Kind: core.KindInput, Content: "check disk"}, // turn 1 (old)
		toolResult(big), // turn 1, large -> elided
		{Role: provider.RoleAssistant, Kind: core.KindOutput, Content: "still old"}, // turn 1
		{Role: provider.RoleUser, Kind: core.KindInput, Content: "now fix it"},      // turn 2 (current)
		toolResult(big), // turn 2, large -> kept whole
		{Role: provider.RoleAssistant, Kind: core.KindOutput, Content: "done"}, // turn 2
	}

	out := CompactHistory(history, c)
	if len(out) != len(history) {
		t.Fatalf("length changed: %d", len(out))
	}
	if strings.Contains(out[1].Content, "row") {
		t.Errorf("old tool result not elided: %q", out[1].Content)
	}
	if !strings.Contains(out[1].Content, "COMMAND: df -h") {
		t.Errorf("elision dropped the command header: %q", out[1].Content)
	}
	if !strings.Contains(out[1].Content, "elided") {
		t.Errorf("elision marker missing: %q", out[1].Content)
	}
	if out[0].Content != "check disk" || out[3].Content != "now fix it" {
		t.Errorf("a user instruction was touched: %q / %q", out[0].Content, out[3].Content)
	}
	if out[4].Content != big {
		t.Error("the current turn's observation must be kept verbatim")
	}
	if history[1].Content != big {
		t.Error("CompactHistory mutated its input")
	}
}

// Nothing happens until the conversation has more turns than the trigger.
func TestCompactHistoryRespectsTrigger(t *testing.T) {
	c := config.ContextConfig{KeepTurns: 1, ElideAfterTurns: 2, ElideMinBytes: 1, ElideHeadLines: 3}
	big := strings.Repeat("x\n", 50)
	turn := func(name string) []provider.Message {
		return []provider.Message{
			{Role: provider.RoleUser, Kind: core.KindInput, Content: name},
			toolResult(big),
		}
	}
	two := append(turn("one"), turn("two")...)
	if out := CompactHistory(two, c); out[1].Content != big {
		t.Error("compaction ran at the trigger instead of above it")
	}
	three := append(two, turn("three")...)
	if out := CompactHistory(three, c); out[1].Content == big {
		t.Error("a turn beyond the trigger was not elided")
	}
}

func TestCompactHistoryLeavesSmallObservations(t *testing.T) {
	c := config.ContextConfig{KeepTurns: 0, ElideAfterTurns: 0, ElideMinBytes: 100, ElideHeadLines: 3}
	small := "[cmd result]\nCOMMAND: ls\nOUTPUT:\nok"
	out := CompactHistory([]provider.Message{toolResult(small)}, c)
	if out[0].Content != small {
		t.Errorf("small observation was elided: %q", out[0].Content)
	}
}

// Elision is deterministic and silent on the wire; DescribeCompaction reports its
// footprint so the role loop can show it in the context log, and reports nothing
// when there is nothing old enough to elide.
func TestDescribeCompactionReportsFootprint(t *testing.T) {
	c := config.ContextConfig{KeepTurns: 1, ElideAfterTurns: 0, ElideMinBytes: 10, ElideHeadLines: 3}
	big := "[cmd result]\nCOMMAND: df -h\nEXIT_ERROR: <nil>\nOUTPUT:\n" + strings.Repeat("row\n", 100)

	history := []provider.Message{
		{Role: provider.RoleUser, Kind: core.KindInput, Content: "check disk"},
		toolResult(big),
		{Role: provider.RoleUser, Kind: core.KindInput, Content: "now fix it"},
	}
	note := DescribeCompaction(history, c)
	if !strings.Contains(note, "elided 1 older tool observation") {
		t.Errorf("note should report the elision: %q", note)
	}
	if DescribeCompaction(history[:1], c) != "" {
		t.Error("nothing old enough should report nothing")
	}
}

func summaryTurn(body string) core.Turn {
	return core.Turn{Role: "system", Kind: core.KindSummary, Content: "[conversation summary so far]\n" + body}
}

func turn(kind, content string) core.Turn { return core.Turn{Kind: kind, Content: content} }

// A summary checkpoint stands in for everything before it, so replaying a session
// sends the summary plus only the turns after it — not the turns it summarizes.
func TestReplayTurnsStartsAtSummary(t *testing.T) {
	turns := []core.Turn{
		turn("input", "old request"),
		turn("output", "old reply"),
		turn("tool_result", "old observation"),
		summaryTurn("what happened so far"),
		turn("input", "new request"),
		turn("output", "new reply"),
	}

	got := ReplayTurns(turns, 0)
	if len(got) != 3 {
		t.Fatalf("got %d turns, want summary + 2", len(got))
	}
	if got[0].Kind != core.KindSummary {
		t.Errorf("first turn = %q, want the summary", got[0].Kind)
	}
	if got[1].Content != "new request" {
		t.Errorf("replayed a summarized turn: %q", got[1].Content)
	}
}

// The newest checkpoint wins; older ones are superseded and dropped.
func TestReplayTurnsUsesLastSummary(t *testing.T) {
	turns := []core.Turn{
		summaryTurn("first"),
		turn("input", "middle"),
		summaryTurn("second"),
		turn("input", "latest"),
	}
	got := ReplayTurns(turns, 0)
	if len(got) != 2 || !strings.Contains(got[0].Content, "second") {
		t.Fatalf("expected the second summary + latest, got %+v", got)
	}
}

// max_turns caps the tail but never discards the checkpoint itself.
func TestReplayTurnsCapsTailKeepingSummary(t *testing.T) {
	turns := []core.Turn{
		turn("input", "old"),
		summaryTurn("so far"),
		turn("input", "a"),
		turn("output", "b"),
		turn("input", "c"),
	}
	got := ReplayTurns(turns, 2)
	if len(got) != 3 {
		t.Fatalf("got %d turns, want summary + 2", len(got))
	}
	if got[0].Kind != core.KindSummary {
		t.Errorf("summary was dropped by max_turns: %+v", got)
	}
	if got[1].Content != "b" || got[2].Content != "c" {
		t.Errorf("tail = %q,%q, want b,c", got[1].Content, got[2].Content)
	}
}

// Without a checkpoint, replay is the whole transcript capped to the tail.
func TestReplayTurnsWithoutSummary(t *testing.T) {
	turns := []core.Turn{turn("input", "a"), turn("output", "b"), turn("input", "c")}

	if got := ReplayTurns(turns, 0); len(got) != 3 {
		t.Errorf("max_turns=0 should replay everything, got %d", len(got))
	}
	got := ReplayTurns(turns, 2)
	if len(got) != 2 || got[0].Content != "b" {
		t.Errorf("tail = %+v, want b,c", got)
	}
}

// ContextCards reports, per stored turn, what a request would send and why.
func TestContextCardsStates(t *testing.T) {
	big := "[cmd result]\nCOMMAND: df -h\nOUTPUT:\n" + strings.Repeat("row\n", 50)
	cfg := config.ContextConfig{KeepTurns: 1, ElideMinBytes: 10, ElideHeadLines: 2}
	turns := []core.Turn{
		{Seq: 1, Role: "user", Kind: core.KindInput, Content: "pre"},
		{Seq: 2, Role: "user", Kind: core.KindToolResult, Content: "[cmd result]\nok"},
		{Seq: 3, Role: "system", Kind: core.KindSummary, Content: "[conversation summary so far]\nx"},
		{Seq: 4, Role: "user", Kind: core.KindInput, Content: "old task"},
		{Seq: 5, Role: "user", Kind: core.KindToolResult, Content: big},
		{Seq: 6, Role: "user", Kind: core.KindInput, Content: "now"},
	}
	cards := ContextCards(turns, nil, cfg, 0)
	want := map[int]string{
		1: StateSummarizedAway, 2: StateSummarizedAway, 3: StateSent,
		4: StateSent, 5: StateElided, 6: StateSent,
	}
	for _, c := range cards {
		if want[c.Seq] != c.State {
			t.Errorf("seq %d state = %q, want %q", c.Seq, c.State, want[c.Seq])
		}
	}
	elided := cards[4]
	if elided.Original != big || len(elided.Sent) >= len(big) || !strings.Contains(elided.Sent, "elided") {
		t.Errorf("elided card should keep the original and show the trimmed send: %+v", elided)
	}
	for _, c := range cards {
		if c.State == StateSummarizedAway && c.Sent != "" {
			t.Errorf("a summarized-away card must not send anything: %+v", c)
		}
	}
}

// max_turns caps the replayed tail; those turns are reported as capped, not sent.
func TestContextCardsCapped(t *testing.T) {
	cfg := config.ContextConfig{}
	turns := []core.Turn{
		{Seq: 1, Kind: core.KindInput, Content: "a"},
		{Seq: 2, Kind: core.KindOutput, Content: "b"},
		{Seq: 3, Kind: core.KindInput, Content: "c"},
		{Seq: 4, Kind: core.KindOutput, Content: "d"},
	}
	cards := ContextCards(turns, nil, cfg, 2)
	if cards[0].State != StateCapped || cards[1].State != StateCapped {
		t.Errorf("the first two should be capped: %+v", cards)
	}
	if cards[2].State != StateSent || cards[3].State != StateSent {
		t.Errorf("the last two should be sent: %+v", cards)
	}
}

// An override edits or drops a turn; a replaced body is what gets sent.
func TestContextCardsOverrides(t *testing.T) {
	cfg := config.ContextConfig{KeepTurns: 1, ElideMinBytes: 100}
	turns := []core.Turn{
		{Seq: 1, Kind: core.KindInput, Content: "a"},
		{Seq: 2, Kind: core.KindToolResult, Content: "long output"},
		{Seq: 3, Kind: core.KindInput, Content: "b"},
	}
	overrides := map[int]Override{
		1: {Action: core.OverrideDrop},
		2: {Action: core.OverrideReplace, Content: "short"},
	}
	cards := ContextCards(turns, overrides, cfg, 0)
	if cards[0].State != StateDropped || cards[0].Sent != "" {
		t.Errorf("dropped card = %+v", cards[0])
	}
	if cards[1].State != StateEdited || cards[1].Sent != "short" || cards[1].Original != "long output" {
		t.Errorf("edited card = %+v", cards[1])
	}
	if cards[2].State != StateSent {
		t.Errorf("untouched card = %+v", cards[2])
	}
}
