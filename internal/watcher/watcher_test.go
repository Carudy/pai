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
		Name: "tick", Kind: "timer", Every: time.Minute,
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
}

func TestChangedTriggerSeedsThenFiresOnce(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	spec := config.WatcherSpec{
		Name: "level", Kind: "command", Cmd: "cat value.txt",
		Every: time.Minute, Timeout: 5 * time.Second,
		Trigger: &config.WatchTrigger{Kind: "changed"},
	}
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)

	writeValue(t, dir, "1")
	first := Probe(ctx, spec, dir, "", now)
	if first.Fire {
		t.Fatal("the first observation only seeds the baseline")
	}
	st := readState(t, first.State)
	if st.Value != "1" {
		t.Fatalf("baseline = %q, want 1", st.Value)
	}

	second := Probe(ctx, spec, dir, first.State, now.Add(time.Minute))
	if second.Fire {
		t.Fatal("an unchanged value must not fire")
	}

	writeValue(t, dir, "2")
	third := Probe(ctx, spec, dir, second.State, now.Add(2*time.Minute))
	if !third.Fire {
		t.Fatal("a changed value should fire")
	}
	if !strings.Contains(third.Summary, "changed to 2") {
		t.Fatalf("summary = %q", third.Summary)
	}
	st = readState(t, third.State)
	if st.Value != "2" || st.Pending != "" || st.Since != 0 {
		t.Fatalf("state after fire = %+v, want the new baseline and no pending change", st)
	}

	fourth := Probe(ctx, spec, dir, third.State, now.Add(3*time.Minute))
	if fourth.Fire {
		t.Fatal("the same value must not fire twice")
	}
}

func TestChangedTriggerHonoursDebounce(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	spec := config.WatcherSpec{
		Name: "level", Kind: "command", Cmd: "cat value.txt",
		Every: time.Minute, Timeout: 5 * time.Second, Debounce: 30 * time.Second,
		Trigger: &config.WatchTrigger{Kind: "changed"},
	}
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	writeValue(t, dir, "1")
	seeded := Probe(ctx, spec, dir, "", now)
	writeValue(t, dir, "2")

	early := Probe(ctx, spec, dir, seeded.State, now.Add(10*time.Second))
	if early.Fire {
		t.Fatal("a change younger than the debounce window must not fire")
	}
	mid := Probe(ctx, spec, dir, early.State, now.Add(20*time.Second))
	if mid.Fire {
		t.Fatal("the change is still younger than the window")
	}
	late := Probe(ctx, spec, dir, mid.State, now.Add(41*time.Second))
	if !late.Fire {
		t.Fatal("a change that persisted for the window should fire")
	}
}

func TestJSONNumberTriggerAndCooldown(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	spec := config.WatcherSpec{
		Name: "river", Kind: "command", Cmd: `echo '{"river":{"level":3.4}}'`,
		Every: time.Minute, Timeout: 5 * time.Second, Cooldown: time.Minute,
		Trigger: &config.WatchTrigger{Kind: "json_number", Field: "river.level", Op: ">", Value: 3.2},
	}
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)

	first := Probe(ctx, spec, dir, "", now)
	if !first.Fire {
		t.Fatal("the threshold was crossed on the first observation")
	}
	if first.Err != nil {
		t.Fatalf("err = %v", first.Err)
	}
	if !strings.Contains(first.Summary, "river.level") {
		t.Fatalf("summary = %q", first.Summary)
	}
	held := Probe(ctx, spec, dir, first.State, now.Add(10*time.Second))
	if held.Fire {
		t.Fatal("cooldown must suppress a repeat while the condition still holds")
	}
	after := Probe(ctx, spec, dir, held.State, now.Add(61*time.Second))
	if !after.Fire {
		t.Fatal("the condition should fire again once the cooldown elapsed")
	}
}

func TestJSONWhitespaceIsNotAChange(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	writeValue(t, dir, `{"a": 1}`)
	spec := config.WatcherSpec{
		Name: "doc", Kind: "command", Cmd: "cat value.txt",
		Every: time.Minute, Timeout: 5 * time.Second,
		Trigger: &config.WatchTrigger{Kind: "changed"},
	}
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	first := Probe(ctx, spec, dir, "", now)
	writeValue(t, dir, `{"a":1}`)
	second := Probe(ctx, spec, dir, first.State, now.Add(time.Minute))
	if second.Fire {
		t.Fatal("reformatting JSON is not a change")
	}
}

func TestProbeFailureFiresOncePerDistinctError(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	writeValue(t, dir, "boom")
	spec := config.WatcherSpec{
		Name: "broken", Kind: "command", Cmd: "cat err.txt; exit 1",
		Every: time.Minute, Timeout: 5 * time.Second,
		Trigger: &config.WatchTrigger{Kind: "changed"},
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
