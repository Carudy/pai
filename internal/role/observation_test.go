package role

import (
	"errors"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/tool"
)

// The observation is what the model re-reads, so its shape is deliberate: the tool
// name and the command share the header (the first lines are all that survives
// elision), the exit status leads the output, and a tool-level error is the only
// extra line. The old EXIT_ERROR/OUTPUT scaffolding is gone.
func TestObservationShape(t *testing.T) {
	trunc := chat.Truncate{MaxBytes: 1000, HeadLines: 80, TailLines: 40}

	got := observation("execute", "df -h", nil, tool.ExecResult{ExitCode: 0, Output: "/dev/disk1\n"}, trunc)
	want := "[tool execute result] df -h\n[exit 0]\n/dev/disk1\n"
	if got != want {
		t.Errorf("observation = %q, want %q", got, want)
	}

	got = observation("execute", "false", errors.New("start process"), tool.ExecResult{ExitCode: 1}, trunc)
	if !strings.HasPrefix(got, "[tool execute result] false\nerror: start process\n[exit 1]") {
		t.Errorf("a tool-level error should be its own line: %q", got)
	}

	// An unknown command still names the tool, so the observation is never anonymous.
	if got := observation("remote", "  ", nil, tool.ExecResult{ExitCode: 0, Output: "ok\n"}, trunc); !strings.HasPrefix(got, "[tool remote result]\n") {
		t.Errorf("empty command should not leave a trailing space: %q", got)
	}

	// A timed-out run keeps the marker tool.ExecResult supplies.
	if got := observation("execute", "sleep 10", nil, tool.ExecResult{TimedOut: true}, trunc); !strings.Contains(got, "[timed out]") {
		t.Errorf("timeout marker lost: %q", got)
	}
}
