package hostinfo

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDescribe(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/fish")
	t.Setenv("USER", "tester")

	born := time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)
	got := Describe("/work/dir", born)
	for _, want := range []string{
		"OS: ",
		"(" + runtime.GOOS + " " + runtime.GOARCH + ")",
		"Shell: fish",
		"User: tester",
		"Working Dir: /work/dir\n",
		"Session started: 2026-01-01 09:30:00\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe missing %q in:\n%s", want, got)
		}
	}
}

// The head is what prefix caching keys on, so it must not carry a clock that ticks.
// The session's birth is the only time it states, and an ephemeral run states none.
func TestDescribeCarriesNoLiveClock(t *testing.T) {
	born := time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)
	if a, b := Describe("/w", born), Describe("/w", born); a != b {
		t.Errorf("Describe is not byte-stable:\n%s\n---\n%s", a, b)
	}
	if got := Describe("/w", time.Time{}); strings.Contains(got, "Session started:") {
		t.Errorf("an ephemeral run should state no start time:\n%s", got)
	}
}

// An unset environment must degrade to placeholders, not empty fields.
func TestDescribeWithoutEnvironment(t *testing.T) {
	t.Setenv("SHELL", "")
	t.Setenv("USER", "")

	if got := Describe("/w", time.Time{}); !strings.Contains(got, "Shell: unknown User: unknown") {
		t.Errorf("missing placeholders:\n%s", got)
	}
}

// An empty workspace reports the process cwd without changing it.
func TestDescribeEmptyWorkingDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got := Describe("", time.Time{}); !strings.Contains(got, "Working Dir: "+wd+"\n") {
		t.Errorf("Describe(\"\") did not report the cwd:\n%s", got)
	}
	if after, _ := os.Getwd(); after != wd {
		t.Errorf("cwd changed: %s", after)
	}
}

func TestOSDetailIsNeverEmpty(t *testing.T) {
	if osDetail() == "" {
		t.Fatal("osDetail returned an empty description")
	}
}
