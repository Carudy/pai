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

func TestCompactHistoryElidesOldToolResults(t *testing.T) {
	c := config.ContextConfig{KeepTurns: 2, ElideAfterTurns: 0, ElideMinBytes: 10, ElideHeadLines: 3}
	big := "[cmd result]\nCOMMAND: df -h\nEXIT_ERROR: <nil>\nOUTPUT:\n" + strings.Repeat("row\n", 100)

	history := []provider.Message{
		toolResult(big), // old, large -> elided
		{Role: provider.RoleUser, Kind: core.KindInput, Content: "please do it"}, // old, but the user's words
		toolResult("[cmd result]\nCOMMAND: ls\nOUTPUT:\nok"),                     // newest -> verbatim
		{Role: provider.RoleAssistant, Kind: core.KindOutput, Content: "done"},   // newest
	}

	out := CompactHistory(history, c)
	if len(out) != len(history) {
		t.Fatalf("length changed: %d", len(out))
	}
	if strings.Contains(out[0].Content, "row") {
		t.Errorf("old tool result not elided: %q", out[0].Content)
	}
	if !strings.Contains(out[0].Content, "COMMAND: df -h") {
		t.Errorf("elision dropped the command header: %q", out[0].Content)
	}
	if !strings.Contains(out[0].Content, "elided") {
		t.Errorf("elision marker missing: %q", out[0].Content)
	}
	if out[1].Content != "please do it" {
		t.Errorf("user input was touched: %q", out[1].Content)
	}
	if strings.Contains(out[2].Content, "elided") {
		t.Errorf("most recent tool result must be kept: %q", out[2].Content)
	}
	if history[0].Content != big {
		t.Error("CompactHistory mutated its input")
	}
}

// Nothing happens until the conversation is long enough to matter.
func TestCompactHistoryRespectsTrigger(t *testing.T) {
	c := config.ContextConfig{KeepTurns: 1, ElideAfterTurns: 10, ElideMinBytes: 1, ElideHeadLines: 3}
	big := strings.Repeat("x\n", 50)
	out := CompactHistory([]provider.Message{toolResult(big)}, c)
	if out[0].Content != big {
		t.Error("compaction ran below the trigger")
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
