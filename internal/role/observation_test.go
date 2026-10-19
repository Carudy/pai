package role

import (
	"errors"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/tool"
)

// The observation is what the model re-reads, so its shape is deliberate: one header
// line carrying the tool and its outcome (the first lines are all that survives
// elision, so they must say both), and then the output alone. The command lives in the
// action before it, which is never elided.
func TestObservationShape(t *testing.T) {
	trunc := chat.Truncate{MaxBytes: 1000, HeadLines: 80, TailLines: 40}

	got := observation("exec", nil, tool.ExecResult{ExitCode: 0, Output: "/dev/disk1\n"}, trunc)
	want := "[exec result]: exit code 0\n/dev/disk1\n"
	if got != want {
		t.Errorf("observation = %q, want %q", got, want)
	}

	got = observation("exec", nil, tool.ExecResult{ExitCode: 2, Output: "boom\n"}, trunc)
	if !strings.HasPrefix(got, "[exec result]: exit code 2\n") {
		t.Errorf("a failing command keeps its code on the header: %q", got)
	}

	got = observation("exec", errors.New("start process"), tool.ExecResult{ExitCode: -1}, trunc)
	if !strings.HasPrefix(got, "[exec result]: error: start process\n") {
		t.Errorf("a tool-level error belongs on the header: %q", got)
	}

	// A timed-out run keeps the marker even though it also carries an error.
	got = observation("exec", errors.New("signal: killed"), tool.ExecResult{TimedOut: true, Output: "partial\n"}, trunc)
	if !strings.HasPrefix(got, "[exec result]: timed out\npartial\n") {
		t.Errorf("timeout should win over the error text: %q", got)
	}

	// The body is the output alone: no exit line, no command, no OUTPUT label.
	if strings.Contains(got, "[exit") || strings.Contains(got, "COMMAND") || strings.Contains(got, "OUTPUT:") {
		t.Errorf("old scaffolding leaked into the observation: %q", got)
	}

	// A remote run is named after its own tool.
	if got := observation("remote", nil, tool.ExecResult{ExitCode: 0}, trunc); !strings.HasPrefix(got, "[remote result]: exit code 0\n") {
		t.Errorf("remote header = %q", got)
	}
}
