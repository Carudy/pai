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

// Elision keeps a head and a tail and drops only what lies between them: the head
// names the command, the tail carries the error or summary the model reasons about.
// Output that already fits the budget is untouched.
func TestElideObservationKeepsHeadAndTail(t *testing.T) {
	content := strings.Join([]string{"h1", "h2", "h3", "h4", "m1", "m2", "m3", "t1", "t2"}, "\n")
	got, dropped := elideObservation(content, 2, 2)
	want := "h1\nh2\n" + elisionMarker + " [5 lines of earlier output elided; re-run the command if you need them]\nt1\nt2"
	if got != want || dropped != 5 {
		t.Errorf("elideObservation = %q (dropped %d), want %q (dropped 5)", got, dropped, want)
	}

	if got, dropped := elideObservation("a\nb\nc\nd", 2, 2); got != "a\nb\nc\nd" || dropped != 0 {
		t.Errorf("content within the budget was rewritten: %q (dropped %d)", got, dropped)
	}

	// A zero tail keeps the head-only shape, which is what a caller that wants no tail
	// lines gets.
	if got, _ := elideObservation(content, 2, 0); !strings.HasSuffix(got, "need them]") {
		t.Errorf("a zero tail should end at the marker: %q", got)
	}
}

// Turns, not messages, decide the verbatim window: an old turn's large
// observation is elided, but the whole current turn is kept however many tool
// calls it took, so the model never loses the task it is in the middle of.
func TestCompactHistoryElidesOnlyOldTurns(t *testing.T) {
	c := config.ContextConfig{KeepTurns: 1, ElideAfterTokens: 1, ElideMinBytes: 10, ElideHeadLines: 3, ElideTailLines: 2}
	big := "[tool execute result] df -h\n[exit 0]\n" + strings.Repeat("row\n", 100)

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
	// The shape is head, marker, tail: the header names what ran and the tail keeps the
	// last lines, where errors and summaries land. Everything between them is gone.
	want := "[tool execute result] df -h\n[exit 0]\nrow\n" +
		elisionMarker + " [98 lines of earlier output elided; re-run the command if you need them]\nrow\n"
	if out[1].Content != want {
		t.Errorf("elided observation = %q, want %q", out[1].Content, want)
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

// Nothing happens until the history reaches the size trigger.
func TestCompactHistoryRespectsTrigger(t *testing.T) {
	big := strings.Repeat("x\n", 4000) // 8000 bytes, about 2000 tokens
	turn := func(name string) []provider.Message {
		return []provider.Message{
			{Role: provider.RoleUser, Kind: core.KindInput, Content: name},
			toolResult(big),
		}
	}
	var history []provider.Message
	for i := range 12 {
		history = append(history, turn("task "+strings.Repeat("x", i))...)
	}
	size := EstimateTokens(history)

	above := config.ContextConfig{KeepTurns: 1, ElideAfterTokens: size + 1, ElideMinBytes: 1, ElideHeadLines: 3}
	if _, info := compact(history, above); info.Observations != 0 {
		t.Errorf("compaction ran below the size trigger: %+v", info)
	}
	at := above
	at.ElideAfterTokens = size
	if _, info := compact(history, at); info.Observations == 0 {
		t.Error("a history at the trigger was not elided")
	}
}

// Above the budget the front moves in whole batches, so a turn that leaves the window
// alone does not rewrite the prompt: one more turn changes nothing until a full batch
// has accumulated, and then the front jumps.
func TestCompactHistoryBatchesTheFront(t *testing.T) {
	big := strings.Repeat("x\n", 4000) // 8000 bytes, about 2000 tokens
	build := func(old int) []provider.Message {
		msgs := make([]provider.Message, 0, 2*old+1)
		for i := range old {
			msgs = append(msgs,
				provider.Message{Role: provider.RoleUser, Kind: core.KindInput, Content: "t" + strings.Repeat("x", i)},
				toolResult(big))
		}
		return append(msgs, provider.Message{Role: provider.RoleUser, Kind: core.KindInput, Content: "now"})
	}
	// A 32k trigger makes one batch 4k tokens — two old turns' worth of output.
	c := config.ContextConfig{KeepTurns: 1, ElideAfterTokens: 32000, ElideMinBytes: 1, ElideHeadLines: 2}
	elided := func(old int) (int, []provider.Message) {
		out, info := compact(build(old), c)
		return info.Observations, out
	}

	first, out := elided(20)
	if first == 0 {
		t.Fatal("a 40k history should have elided something")
	}
	second, _ := elided(21)
	if second != first {
		t.Errorf("one more turn moved the front: %d -> %d elided", first, second)
	}
	// The turn just outside the keep window is still verbatim, which is what batching
	// buys at the price of a little more context.
	if out[len(out)-2].Content != big {
		t.Error("the newest old observation should still be verbatim")
	}

	// Two more turns are a whole batch, so the front catches up with them.
	if later, _ := elided(25); later <= second {
		t.Errorf("a whole batch did not move the front: %d -> %d elided", second, later)
	}
}

func TestCompactHistoryLeavesSmallObservations(t *testing.T) {
	c := config.ContextConfig{KeepTurns: 0, ElideAfterTokens: 1, ElideMinBytes: 100, ElideHeadLines: 3}
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
	c := config.ContextConfig{KeepTurns: 1, ElideAfterTokens: 1, ElideMinBytes: 10, ElideHeadLines: 3}
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

// A manual compaction with nothing older than the verbatim window says which case
// it is, rather than exiting early in silence.
func TestDescribeNoCompaction(t *testing.T) {
	short := []provider.Message{
		{Role: provider.RoleUser, Kind: core.KindInput, Content: "a"},
		{Role: provider.RoleAssistant, Kind: core.KindOutput, Content: "b"},
		{Role: provider.RoleUser, Kind: core.KindInput, Content: "c"},
	}
	if got := DescribeNoCompaction(short, 3); !strings.Contains(got, "inside the keep_turns = 3 window") {
		t.Errorf("short session = %q", got)
	}

	long := append(append([]provider.Message{}, short...),
		provider.Message{Role: provider.RoleUser, Kind: core.KindInput, Content: "d"},
		provider.Message{Role: provider.RoleAssistant, Kind: core.KindOutput, Content: "e"},
		provider.Message{Role: provider.RoleUser, Kind: core.KindInput, Content: "f"},
		provider.Message{Role: provider.RoleAssistant, Kind: core.KindOutput, Content: "g"})
	if got := DescribeNoCompaction(long, 3); got != "" {
		t.Errorf("a compactable session should explain nothing, got %q", got)
	}
	if got := UserTurns(long); got != 4 {
		t.Errorf("UserTurns = %d, want 4", got)
	}

	// With a checkpoint, the span can be nothing but the previous summary: folding
	// that into a new summary would cost a model call and lose detail.
	onlySummary := []provider.Message{
		{Role: provider.RoleSystem, Kind: core.KindSummary, Content: "[conversation summary so far]\nx"},
		{Role: provider.RoleUser, Kind: core.KindInput, Content: "a"},
		{Role: provider.RoleAssistant, Kind: core.KindOutput, Content: "b"},
		{Role: provider.RoleUser, Kind: core.KindInput, Content: "c"},
		{Role: provider.RoleAssistant, Kind: core.KindOutput, Content: "d"},
	}
	if got := DescribeNoCompaction(onlySummary, 2); !strings.Contains(got, "would only lose detail") {
		t.Errorf("summary-only span = %q", got)
	}
}

func summaryTurn(body string) core.Turn {
	return core.Turn{Role: "system", Kind: core.KindSummary, Content: "[conversation summary so far]\n" + body}
}

func turn(kind, content string) core.Turn { return core.Turn{Kind: kind, Content: content} }

// A summary checkpoint stands in for everything before it, so replaying a session
// sends the summary plus only the turns after it — not the turns it summarizes.
// keepTurns 0 means compaction left no verbatim window in front of the checkpoint.
func TestReplayTurnsStartsAtSummary(t *testing.T) {
	turns := []core.Turn{
		turn("input", "old request"),
		turn("output", "old reply"),
		turn("tool_result", "old observation"),
		summaryTurn("what happened so far"),
		turn("input", "new request"),
		turn("output", "new reply"),
	}

	got := ReplayTurns(turns, 0, 0)
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

// Compaction appends its checkpoint, so it lands after the keepTurns user turns it
// kept verbatim and deliberately did not summarize. Those turns must be replayed
// too: the summary does not cover them, so a replay that started at the checkpoint
// alone would drop the most recent work on every resume. The shape below is a real
// session compacted right after "check my nix installed apps".
func TestReplayTurnsReplaysUnsummarizedWindow(t *testing.T) {
	turns := []core.Turn{
		turn("input", "check my proxy settings"),
		turn("output", "here they are"),
		turn("input", "list tools u can invoke"),
		turn("input", "note that our hostname is bogon"),
		turn("input", "yes; change for me"),
		turn("input", "check my nix installed apps"),
		turn("output", "Nix-installed apps"),
		summaryTurn("the proxy discussion"),
	}

	got := ReplayTurns(turns, 3, 0)
	want := []string{
		"[conversation summary so far]\nthe proxy discussion",
		"note that our hostname is bogon",
		"yes; change for me",
		"check my nix installed apps",
		"Nix-installed apps",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d turns, want %d: %+v", len(got), len(want), got)
	}
	for i, content := range want {
		if got[i].Content != content {
			t.Errorf("turn %d = %q, want %q", i, got[i].Content, content)
		}
	}
	for _, replayed := range got {
		if strings.Contains(replayed.Content, "proxy settings") {
			t.Errorf("replayed a turn the summary covers: %q", replayed.Content)
		}
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
	got := ReplayTurns(turns, 0, 0)
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
	got := ReplayTurns(turns, 0, 2)
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

	if got := ReplayTurns(turns, 0, 0); len(got) != 3 {
		t.Errorf("max_turns=0 should replay everything, got %d", len(got))
	}
	got := ReplayTurns(turns, 0, 2)
	if len(got) != 2 || got[0].Content != "b" {
		t.Errorf("tail = %+v, want b,c", got)
	}
}

// ContextCards reports, per stored turn, what a request would send and why. The
// fixture has the shape a real compaction leaves behind: the span it summarized,
// then the keepTurns turns it kept verbatim, then the checkpoint, then the turns
// recorded after it.
func TestContextCardsStates(t *testing.T) {
	big := "[cmd result]\nCOMMAND: df -h\nOUTPUT:\n" + strings.Repeat("row\n", 50)
	cfg := config.ContextConfig{KeepTurns: 1, ElideAfterTokens: 1, ElideMinBytes: 10, ElideHeadLines: 2, ElideTailLines: 1}
	turns := []core.Turn{
		{Seq: 1, Role: "user", Kind: core.KindInput, Content: "pre"},
		{Seq: 2, Role: "user", Kind: core.KindToolResult, Content: "[cmd result]\nok"},
		{Seq: 3, Role: "user", Kind: core.KindInput, Content: "old task"},
		{Seq: 4, Role: "user", Kind: core.KindToolResult, Content: big},
		{Seq: 5, Role: "system", Kind: core.KindSummary, Content: "[conversation summary so far]\nx"},
		{Seq: 6, Role: "user", Kind: core.KindInput, Content: "now"},
	}
	cards := ContextCards(turns, nil, cfg, 0)
	want := map[int]string{
		1: StateSummarizedAway, 2: StateSummarizedAway,
		3: StateSent, 4: StateElided, 5: StateSent, 6: StateSent,
	}
	for _, c := range cards {
		if want[c.Seq] != c.State {
			t.Errorf("seq %d state = %q, want %q", c.Seq, c.State, want[c.Seq])
		}
	}
	elided := cards[3]
	if elided.State != StateElided || elided.Original != big || len(elided.Sent) >= len(big) || !strings.Contains(elided.Sent, "elided") {
		t.Errorf("elided card should keep the original and show the trimmed send: %+v", elided)
	}
	for _, c := range cards {
		if c.State == StateSummarizedAway && c.Sent != "" {
			t.Errorf("a summarized-away card must not send anything: %+v", c)
		}
	}
}

// A turn the model over-ran is reported as its own state: it *is* sent, but shortened,
// and the inspector has to say which of the two shortenings happened.
func TestContextCardsReportAnOverRunTurn(t *testing.T) {
	cfg := config.ContextConfig{KeepTurns: 1}
	turns := []core.Turn{
		{Seq: 1, Role: "user", Kind: core.KindInput, Content: "go"},
		{Seq: 2, Role: "assistant", Kind: core.KindOutput, Content: actionA + "\n[cmd result]\nfake\n" + actionB},
	}
	cards := ContextCards(turns, nil, cfg, 0)
	if cards[1].State != StateTrimmed {
		t.Errorf("state = %q, want %q", cards[1].State, StateTrimmed)
	}
	if cards[1].Sent != actionA || cards[1].Original == cards[1].Sent {
		t.Errorf("the card should keep the original and send the action: %+v", cards[1])
	}
	if cards[0].State != StateSent {
		t.Errorf("an untouched turn should stay sent: %+v", cards[0])
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
	cfg := config.ContextConfig{KeepTurns: 1, ElideAfterTokens: 1, ElideMinBytes: 100}
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
