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

// A repeat_after reminder counts from when the model finished handling the wake,
// not from when it fired: a long activation must not come back with a reminder
// it has already earned by working.
func TestRepeatAfterMeasuresFromHandling(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	spec := config.WatcherSpec{
		Name: "river", Command: "cat value.txt",
		Interval: time.Minute, Timeout: 5 * time.Second, RepeatAfter: 2 * time.Minute,
	}
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	writeValue(t, dir, "1")

	first := Probe(ctx, spec, dir, "", now)
	if !first.Fire {
		t.Fatal("the first true observation should fire")
	}
	if _, open := Open(first.State); !open {
		t.Fatal("a fire is open until it is handled")
	}

	handled := MarkHandled(first.State, now.Add(90*time.Second))
	if _, open := Open(handled); open {
		t.Fatal("a handled fire is closed")
	}

	// 100s after the fire but only 10s after handling: no reminder yet.
	if res := Probe(ctx, spec, dir, handled, now.Add(100*time.Second)); res.Fire {
		t.Fatal("repeat_after must be measured from handling, not from firing")
	}
	// 120s after handling: the reminder is due, and it reopens the item.
	res := Probe(ctx, spec, dir, handled, now.Add(210*time.Second))
	if !res.Fire {
		t.Fatal("the reminder should fire once repeat_after elapsed from handling")
	}
	if _, open := Open(res.State); !open {
		t.Fatal("a reminder reopens the item")
	}
}

// A false sample ends the episode and forgets its acknowledgment, so the next
// true sample is a new incident rather than a suppressed repeat.
func TestFalseClosesAnAcknowledgedEpisode(t *testing.T) {
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
	handled := MarkHandled(first.State, now.Add(5*time.Second))
	writeValue(t, dir, "0")
	closed := Probe(ctx, spec, dir, handled, now.Add(time.Minute))
	if closed.Fire {
		t.Fatal("a false predicate does not fire")
	}
	if st := readState(t, closed.State); st.Reported || st.Acknowledged || st.Handled != 0 || st.Fired != 0 {
		t.Fatalf("a false sample must forget the episode: %+v", st)
	}

	writeValue(t, dir, "1")
	if reopened := Probe(ctx, spec, dir, closed.State, now.Add(2*time.Minute)); !reopened.Fire {
		t.Fatal("a new episode should fire again")
	}
}

// MarkHandled is fed whatever the store holds, including a timer's empty state
// or a stale row; it must not invent work out of either.
func TestMarkHandledIgnoresIdleWatcher(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, prev := range []string{"", "{}", `{"error":"boom"}`} {
		if got := MarkHandled(prev, now); got != prev {
			t.Fatalf("MarkHandled(%q) = %q, want it unchanged", prev, got)
		}
		if _, open := Open(prev); open {
			t.Fatalf("Open(%q) = true, want no outstanding work", prev)
		}
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
