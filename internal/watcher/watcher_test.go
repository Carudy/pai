package watcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/config"
)

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("probe commands use a POSIX shell")
	}
}

// writeValue writes what a `cat value.txt` probe will observe.
func writeValue(t *testing.T, dir, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "value.txt"), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readState(t *testing.T, s string) state {
	t.Helper()
	var st state
	if err := json.Unmarshal([]byte(s), &st); err != nil {
		t.Fatalf("state %q: %v", s, err)
	}
	return st
}

func TestTimerProbeAlwaysFires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	res := Probe(context.Background(), config.WatcherSpec{
		Name: "tick", Interval: time.Minute,
	}, t.TempDir(), "", now)

	if !res.Fire {
		t.Fatal("a timer probe should fire on every due run")
	}
	if !res.NextRun.Equal(now.Add(time.Minute)) {
		t.Fatalf("next run = %v, want %v", res.NextRun, now.Add(time.Minute))
	}
	if res.Err != nil {
		t.Fatalf("err = %v", res.Err)
	}
	if res.Summary != "scheduled tick" {
		t.Fatalf("summary = %q, want the default", res.Summary)
	}
}

func TestTimerPromptIsTheSummary(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	res := Probe(context.Background(), config.WatcherSpec{
		Name: "report", Interval: time.Minute,
		Prompt: "Report the current state in one line.",
	}, t.TempDir(), "", now)
	if !res.Fire || res.Summary != "Report the current state in one line." {
		t.Fatalf("fire=%v summary=%q", res.Fire, res.Summary)
	}
}

func TestPredicateDecisions(t *testing.T) {
	skipOnWindows(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)

	for _, tc := range []struct {
		output string
		fire   bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"0", false},
		{"false", false},
	} {
		spec := config.WatcherSpec{
			Name: "check", Command: "printf '%s\\n' " + tc.output,
			Interval: time.Minute, Timeout: 5 * time.Second,
		}
		res := Probe(ctx, spec, t.TempDir(), "", now)
		if res.Err != nil {
			t.Fatalf("%q: err = %v", tc.output, res.Err)
		}
		if res.Fire != tc.fire {
			t.Fatalf("%q: fire = %v, want %v", tc.output, res.Fire, tc.fire)
		}
	}
}

func TestSummaryFollowsTheDecision(t *testing.T) {
	skipOnWindows(t)
	spec := config.WatcherSpec{
		Name:     "river",
		Command:  `printf '1 mode=flood level=3.7\nextra detail\n'`,
		Interval: time.Minute, Timeout: 5 * time.Second,
	}
	res := Probe(context.Background(), spec, t.TempDir(), "", time.Unix(1_700_000_000, 0))
	if !res.Fire || res.Err != nil {
		t.Fatalf("fire = %v, err = %v", res.Fire, res.Err)
	}
	if strings.Contains(res.Summary, "1 mode") || !strings.Contains(res.Summary, "mode=flood level=3.7") {
		t.Fatalf("summary = %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "extra detail") {
		t.Fatalf("summary lost the second line: %q", res.Summary)
	}
}

func TestBareDecisionHasAFallbackSummary(t *testing.T) {
	skipOnWindows(t)
	spec := config.WatcherSpec{
		Name: "river", Command: "echo 1",
		Interval: time.Minute, Timeout: 5 * time.Second,
	}
	res := Probe(context.Background(), spec, t.TempDir(), "", time.Unix(1_700_000_000, 0))
	if res.Summary != "triggered" {
		t.Fatalf("summary = %q, want the fallback", res.Summary)
	}
}

func TestHoldForDebounces(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	spec := config.WatcherSpec{
		Name: "river", Command: "cat value.txt",
		Interval: time.Minute, Timeout: 5 * time.Second, HoldFor: 30 * time.Second,
	}
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	writeValue(t, dir, "1")

	first := Probe(ctx, spec, dir, "", now)
	if first.Fire {
		t.Fatal("the first observation starts the hold window, it does not fire")
	}
	early := Probe(ctx, spec, dir, first.State, now.Add(10*time.Second))
	if early.Fire {
		t.Fatal("a condition younger than hold_for must not fire")
	}
	late := Probe(ctx, spec, dir, early.State, now.Add(41*time.Second))
	if !late.Fire {
		t.Fatal("a condition that held for the window should fire")
	}
}

// Without repeat_after a continuously true predicate fires once per episode:
// the latch clears only when the predicate goes false.
func TestOncePerEpisodeByDefault(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	spec := config.WatcherSpec{
		Name: "river", Command: "cat value.txt",
		Interval: time.Minute, Timeout: 5 * time.Second,
	}
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)

	writeValue(t, dir, "1")
	first := Probe(ctx, spec, dir, "", now)
	if !first.Fire {
		t.Fatal("the first true observation should fire")
	}
	again := Probe(ctx, spec, dir, first.State, now.Add(time.Minute))
	if again.Fire {
		t.Fatal("a still-true predicate must not fire twice in one episode")
	}
	if st := readState(t, again.State); !st.Reported {
		t.Fatalf("state = %+v, want the episode latched", st)
	}

	writeValue(t, dir, "0")
	cleared := Probe(ctx, spec, dir, again.State, now.Add(2*time.Minute))
	if cleared.Fire {
		t.Fatal("a false predicate does not fire")
	}
	writeValue(t, dir, "1")
	reopened := Probe(ctx, spec, dir, cleared.State, now.Add(3*time.Minute))
	if !reopened.Fire {
		t.Fatal("a new episode should fire again")
	}
}

func TestRepeatAfterReAlerts(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	spec := config.WatcherSpec{
		Name: "river", Command: "cat value.txt",
		Interval: time.Minute, Timeout: 5 * time.Second, RepeatAfter: time.Minute,
	}
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	writeValue(t, dir, "1")

	first := Probe(ctx, spec, dir, "", now)
	if !first.Fire {
		t.Fatal("the first true observation should fire")
	}
	held := Probe(ctx, spec, dir, first.State, now.Add(10*time.Second))
	if held.Fire {
		t.Fatal("repeat_after must suppress a repeat before its interval")
	}
	after := Probe(ctx, spec, dir, held.State, now.Add(61*time.Second))
	if !after.Fire {
		t.Fatal("the condition should fire again once repeat_after elapsed")
	}
}

func TestProbeFailureFiresOncePerDistinctError(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	spec := config.WatcherSpec{
		Name: "broken", Command: "cat err.txt; exit 1",
		Interval: time.Minute, Timeout: 5 * time.Second,
	}
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	if err := os.WriteFile(filepath.Join(dir, "err.txt"), []byte("boom"), 0o644); err != nil {
		t.Fatal(err)
	}

	first := Probe(ctx, spec, dir, "", now)
	if !first.Fire || first.Err == nil {
		t.Fatalf("a first failure should wake the model: fire=%v err=%v", first.Fire, first.Err)
	}
	second := Probe(ctx, spec, dir, first.State, now.Add(time.Minute))
	if second.Fire {
		t.Fatal("the same failure must not wake the model again")
	}
	if second.Err == nil {
		t.Fatal("the failure should still be reported")
	}
	if err := os.WriteFile(filepath.Join(dir, "err.txt"), []byte("bang"), 0o644); err != nil {
		t.Fatal(err)
	}
	third := Probe(ctx, spec, dir, second.State, now.Add(2*time.Minute))
	if !third.Fire {
		t.Fatal("a distinct failure is news")
	}
}

func TestInvalidDecisionIsAProbeFailure(t *testing.T) {
	skipOnWindows(t)
	now := time.Unix(1_700_000_000, 0)
	for _, command := range []string{"echo maybe", "true", "printf '\\n\\n'"} {
		spec := config.WatcherSpec{
			Name: "bad", Command: command,
			Interval: time.Minute, Timeout: 5 * time.Second,
		}
		res := Probe(context.Background(), spec, t.TempDir(), "", now)
		if res.Err == nil {
			t.Fatalf("%q: err = nil, want a probe failure", command)
		}
		if !res.Fire {
			t.Fatalf("%q: a first failure should be surfaced", command)
		}
	}
}

func TestNonzeroExitIsAProbeFailure(t *testing.T) {
	skipOnWindows(t)
	spec := config.WatcherSpec{
		Name: "bad", Command: "echo 1; exit 3",
		Interval: time.Minute, Timeout: 5 * time.Second,
	}
	res := Probe(context.Background(), spec, t.TempDir(), "", time.Unix(1_700_000_000, 0))
	if res.Err == nil || !strings.Contains(res.Err.Error(), "exit 3") {
		t.Fatalf("err = %v, want an exit-3 failure", res.Err)
	}
}
