package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/runner"
)

func TestServeDiagnosticLines(t *testing.T) {
	at := time.Date(2026, 10, 27, 14, 30, 0, 0, time.Local)
	next := at.Add(30 * time.Second)
	cases := []struct {
		name string
		d    runner.Diagnostic
		want string
	}{
		{
			"fired",
			runner.Diagnostic{Time: at, Session: "demo", Kind: "watcher", Subject: "level", Outcome: "fired", Detail: "1 level=6.4", Next: next},
			"2026-10-27T14:30:00 [demo] watcher <level>: result \"1 level=6.4\", triggered, next 14:30:30\n",
		},
		{
			"quiet",
			runner.Diagnostic{Time: at, Session: "demo", Kind: "watcher", Subject: "level", Outcome: "quiet", Detail: "0", Next: next},
			"2026-10-27T14:30:00 [demo] watcher <level>: result \"0\", not triggered, next 14:30:30\n",
		},
		{
			"held",
			runner.Diagnostic{Time: at, Session: "demo", Kind: "watcher", Subject: "level", Outcome: "held", Detail: "1 level=6.4", Reason: "already reported this episode", Next: next},
			"2026-10-27T14:30:00 [demo] watcher <level>: result \"1 level=6.4\", not triggered (already reported this episode), next 14:30:30\n",
		},
		{
			"awaiting approval",
			runner.Diagnostic{Time: at, Session: "demo", Kind: "watcher", Subject: "gate", Outcome: "awaiting approval", Detail: "python3 gate.py", Next: next},
			"2026-10-27T14:30:00 [demo] watcher <gate>: awaiting approval: python3 gate.py, next 14:30:30\n",
		},
		{
			"failed",
			runner.Diagnostic{Time: at, Session: "demo", Kind: "watcher", Subject: "gate", Outcome: "failed", Detail: "exit 3: boom"},
			"2026-10-27T14:30:00 [demo] watcher <gate>: failed: exit 3: boom\n",
		},
		{
			"skipped",
			runner.Diagnostic{Time: at, Session: "demo", Kind: "activation", Outcome: "skipped", Detail: "nothing substantive to report", Next: next},
			"2026-10-27T14:30:00 [demo] activation skipped: nothing substantive to report, next 14:30:30\n",
		},
		{
			"error",
			runner.Diagnostic{Time: at, Session: "demo", Kind: "error", Detail: "could not read watchers: boom"},
			"2026-10-27T14:30:00 [demo] error: could not read watchers: boom\n",
		},
		{
			"model",
			runner.Diagnostic{Time: at, Session: "demo", Kind: "model", Subject: "deepseek:flash", Outcome: "usage", Detail: "in 10, out 2"},
			"2026-10-27T14:30:00 [demo] model deepseek:flash usage: in 10, out 2\n",
		},
		{
			"prompt",
			runner.Diagnostic{Time: at, Session: "demo", Kind: "prompt", Outcome: "confirm", Detail: "execute sh"},
			"2026-10-27T14:30:00 [demo] prompt confirm: execute sh\n",
		},
		{
			"service grant",
			runner.Diagnostic{Time: at, Session: "demo", Kind: "service", Subject: "level", Outcome: "granted", Detail: "trusted command: echo 0"},
			"2026-10-27T14:30:00 [demo] service <level> granted: trusted command: echo 0\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := serveDiagnosticLine(tc.d); got != tc.want {
				t.Fatalf("line =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// One diagnostic is one line: probe output may contain newlines and control
// characters, and a log line must not be able to forge another.
func TestServeDiagnosticLineStaysOneLine(t *testing.T) {
	line := serveDiagnosticLine(runner.Diagnostic{
		Time:    time.Date(2026, 10, 27, 14, 30, 0, 0, time.Local),
		Session: "demo", Kind: "watcher", Subject: "level", Outcome: "fired",
		Detail: "line one\nline two\ttabbed\x1b[31mred",
	})
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
		t.Fatalf("line = %q", line)
	}
	if strings.Contains(line, "\t") || strings.Contains(line, "\x1b") {
		t.Fatalf("control characters survived: %q", line)
	}
	if !strings.Contains(line, "line one line two") {
		t.Fatalf("detail was not collapsed: %q", line)
	}
}
