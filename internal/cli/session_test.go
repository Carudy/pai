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
			"command result puts the command in the header",
			core.Turn{Kind: "tool_result", Content: "[tool execute result] echo hi\n[exit 0]\nhi"},
			"[tool execute result] echo hi",
		},
		{
			"older command result still carries its own COMMAND line",
			core.Turn{Kind: "tool_result", Content: "[cmd result]\nCOMMAND: echo hi\nOUTPUT:\nhi"},
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
