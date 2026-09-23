package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/core"
)

func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{12 * time.Second, "12s"},
		{59 * time.Second, "59s"},
		{time.Minute + 14*time.Second, "1m14s"},
		{time.Hour + 3*time.Minute, "1h03m"},
	}
	for _, tc := range cases {
		if got := formatElapsed(tc.d); got != tc.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestToolLiveLabel(t *testing.T) {
	cases := []struct {
		call core.ToolCall
		want string
	}{
		{core.ToolCall{Name: "execute", Detail: "nix profile upgrade '.*'"}, "nix profile upgrade '.*'"},
		{core.ToolCall{Name: "remote", Target: "nb-mini", Detail: "ls"}, "@nb-mini"},
		{core.ToolCall{Name: "websearch", Detail: "nginx 429"}, "nginx 429"},
		{core.ToolCall{Name: "mystery"}, "mystery"},
	}
	for _, tc := range cases {
		if got := toolLiveLabel(tc.call); got != tc.want {
			t.Errorf("toolLiveLabel(%v) = %q, want %q", tc.call, got, tc.want)
		}
	}
}

// A very long command must not blow out the status line.
func TestToolLiveLabelClips(t *testing.T) {
	if got := toolLiveLabel(core.ToolCall{Name: "execute", Detail: "ls"}); got != "ls" {
		t.Fatalf("short label changed: %q", got)
	}
	long := toolLiveLabel(core.ToolCall{Name: "execute", Detail: strings.Repeat("a", 80)})
	if r := []rune(long); len(r) != 49 {
		t.Errorf("label length = %d runes, want 48 + ellipsis", len(r))
	}
}

// A multi-line command is flattened so the status line stays one line.
func TestToolLiveLabelFlattens(t *testing.T) {
	got := toolLiveLabel(core.ToolCall{Name: "execute", Detail: "echo a\necho b"})
	if got != "echo a echo b" {
		t.Errorf("got %q, want the newline flattened", got)
	}
}
