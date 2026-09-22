package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Carudy/pai/internal/core"
)

// renderToolCall renders a tool call to plain text: lipgloss styles each fragment
// separately, so escape sequences sit between them and have to be stripped before
// asserting on the words.
func renderToolCall(c core.ToolCall) string {
	var buf bytes.Buffer
	NewLineObserver(&buf).ToolCall(c)
	return ansi.Strip(buf.String())
}

// A chained command is broken up and numbered, so it can be read and judged
// before approving it.
func TestToolCallRendersSteps(t *testing.T) {
	out := renderToolCall(core.ToolCall{
		Name:   "execute",
		Target: "bash",
		Detail: "ls -l /usr/local/bin/netbird && systemctl is-active netbird; echo '---'",
		Reason: "locate netbird and check the service",
	})

	if !strings.Contains(out, "3 commands:") {
		t.Errorf("missing command count: %q", out)
	}
	for _, want := range []string{"   1 ls -l /usr/local/bin/netbird &&", "   2 systemctl is-active netbird;", "   3 echo '---'"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in: %q", want, out)
		}
	}
}

// A single command stays on one line: numbering it would be noise.
func TestToolCallSingleCommandUnchanged(t *testing.T) {
	out := renderToolCall(core.ToolCall{Name: "execute", Target: "bash", Detail: "df -h"})

	if strings.Contains(out, "commands") {
		t.Errorf("single command should not be numbered: %q", out)
	}
	if !strings.Contains(out, "[CMD 💻 bash] df -h") {
		t.Errorf("unexpected rendering: %q", out)
	}
}

// A very long chain is summarised rather than flooding the transcript.
func TestToolCallLongChainIsCapped(t *testing.T) {
	segs := make([]string, 20)
	for i := range segs {
		segs[i] = "step"
	}
	out := renderToolCall(core.ToolCall{Name: "execute", Target: "bash", Detail: strings.Join(segs, " && ")})

	if !strings.Contains(out, "20 commands:") {
		t.Errorf("missing total count: %q", out)
	}
	if !strings.Contains(out, "+8 more commands") {
		t.Errorf("missing remainder summary: %q", out)
	}
}
