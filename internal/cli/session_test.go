package cli

import (
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/core"
)

func TestDescribeTurn(t *testing.T) {
	cases := []struct {
		name string
		turn core.Turn
		want string
	}{
		{
			"done output",
			core.Turn{Kind: "output", Content: `{"action":"done","payload":"all done","reason":"r"}`},
			"[done] all done",
		},
		{
			"tool output uses the reason",
			core.Turn{Kind: "output", Content: `{"action":"tool","toolname":"execute","payload":"echo hi","reason":"running echo"}`},
			"[tool] running echo",
		},
		{
			"ask output",
			core.Turn{Kind: "output", Content: `{"action":"ask","payload":"which host?","reason":"r"}`},
			"[ask] which host?",
		},
		{
			"unparseable output falls back to raw",
			core.Turn{Kind: "output", Content: "not json at all"},
			"not json at all",
		},
		{
			"command result",
			core.Turn{Kind: "tool_result", Content: "[cmd result]\nCOMMAND: echo hi\nEXIT_ERROR: <nil>\nOUTPUT:\nhi"},
			"[cmd result] echo hi",
		},
		{
			"search result has no COMMAND line",
			core.Turn{Kind: "tool_result", Content: "SEARCH QUERY: nginx 429\nRESULTS:\n..."},
			"SEARCH QUERY: nginx 429",
		},
		{
			"user answer drops the label",
			core.Turn{Kind: "user_answer", Content: "[user answer]\nstaging"},
			"staging",
		},
		{"input is verbatim", core.Turn{Kind: "input", Content: "check disk"}, "check disk"},
		{"note is verbatim", core.Turn{Kind: "note", Content: "[interrupted]"}, "[interrupted]"},
		{
			"summary drops the header label",
			core.Turn{Kind: "summary", Content: "[conversation summary so far]\nran df -h"},
			"[summary] ran df -h",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeTurn(tc.turn, 100); got != tc.want {
				t.Errorf("describeTurn = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDescribeTurnTruncates(t *testing.T) {
	got := describeTurn(core.Turn{Kind: "input", Content: strings.Repeat("x", 500)}, 20)
	if runes := []rune(got); len(runes) != 21 {
		t.Errorf("length = %d runes, want 21 (20 + ellipsis)", len(runes))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncation marker missing: %q", got)
	}
}

func summaryTurn(body string) core.Turn {
	return core.Turn{Role: "system", Kind: core.KindSummary, Content: "[conversation summary so far]\n" + body}
}

func turn(kind, content string) core.Turn { return core.Turn{Kind: kind, Content: content} }

// A summary checkpoint stands in for everything before it, so attaching replays
// the summary plus only the turns after it — not the turns it summarizes.
func TestReplayTurnsStartsAtSummary(t *testing.T) {
	turns := []core.Turn{
		turn("input", "old request"),
		turn("output", "old reply"),
		turn("tool_result", "old observation"),
		summaryTurn("what happened so far"),
		turn("input", "new request"),
		turn("output", "new reply"),
	}

	got := replayTurns(turns, 0)
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
	got := replayTurns(turns, 0)
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
	got := replayTurns(turns, 2)
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

	if got := replayTurns(turns, 0); len(got) != 3 {
		t.Errorf("max_turns=0 should replay everything, got %d", len(got))
	}
	got := replayTurns(turns, 2)
	if len(got) != 2 || got[0].Content != "b" {
		t.Errorf("tail = %+v, want b,c", got)
	}
}
