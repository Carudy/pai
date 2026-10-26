package runner

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
)

func skipWithoutPOSIXShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("probe commands use a POSIX shell")
	}
}

// watchTestBackend adds the watcher port to the service test backend so the
// head-of-activation probe path can be driven directly.
type watchTestBackend struct {
	*serviceTestBackend

	wmu      sync.Mutex
	watchers []WatcherState
	due      []core.Event
}

func (b *watchTestBackend) Watchers(string) ([]WatcherState, error) {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	return append([]WatcherState(nil), b.watchers...), nil
}

func (b *watchTestBackend) PutWatcher(w WatcherState) error {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	if w.ID == "" {
		w.ID = fmt.Sprintf("w-%d", len(b.watchers)+1)
	}
	for i, existing := range b.watchers {
		if existing.Name == w.Name {
			b.watchers[i] = w
			return nil
		}
	}
	b.watchers = append(b.watchers, w)
	return nil
}

func (b *watchTestBackend) SetWatcherState(id string, lastRun, nextRun *time.Time, lastState, lastErr string) error {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	for i, w := range b.watchers {
		if w.ID != id {
			continue
		}
		b.watchers[i].LastRun, b.watchers[i].NextRun = lastRun, nextRun
		b.watchers[i].LastState, b.watchers[i].LastError = lastState, lastErr
		return nil
	}
	return fmt.Errorf("unknown watcher %q", id)
}

func (b *watchTestBackend) DueEvents(string, time.Time, int) ([]core.Event, error) {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	return append([]core.Event(nil), b.due...), nil
}

func (b *watchTestBackend) watcher(t *testing.T, name string) WatcherState {
	t.Helper()
	b.wmu.Lock()
	defer b.wmu.Unlock()
	for _, w := range b.watchers {
		if w.Name == name {
			return w
		}
	}
	t.Fatalf("watcher %q was not stored", name)
	return WatcherState{}
}

func newWatchService(t *testing.T, fp *serviceTestProvider, watchers ...WatcherState) (*Manager, *watchTestBackend) {
	t.Helper()
	backend := &watchTestBackend{
		serviceTestBackend: &serviceTestBackend{
			meta:     Meta{Name: "svc", ID: "sid-w", Mode: ModeService, Cwd: t.TempDir(), CreatedAt: time.Now()},
			provider: fp,
			spec:     `{"goal":"g","role":"devops","workspace":"/w","report_every":"1h"}`,
		},
		watchers: watchers,
	}
	m := New(context.Background(), backend, 2)
	t.Cleanup(m.Close)
	if err := m.StartServices(); err != nil {
		t.Fatal(err)
	}
	return m, backend
}

func clockWake() core.Wake {
	return core.Wake{Reason: "timer", Events: []core.Event{
		{Class: core.EventTimer, Kind: "timer", Source: "clock"},
	}}
}

// A due probe whose trigger did not fire must not spend a model call, but must
// still record what it saw and schedule the next look.
func TestServiceProbeSkipsModelWhenNothingChanged(t *testing.T) {
	skipWithoutPOSIXShell(t)
	past := time.Now().Add(-time.Minute)
	spec := config.WatcherSpec{
		Name: "level", Kind: "command", Cmd: `echo '{"level":1}'`,
		Every: time.Minute, Timeout: 5 * time.Second,
		Trigger: &config.WatchTrigger{Kind: "json_number", Field: "level", Op: ">", Value: 5.0},
	}
	fp := &serviceTestProvider{done: make(chan int, 1)}
	m, backend := newWatchService(t, fp, WatcherState{
		ID: "w1", SessionID: "sid-w", Name: spec.Name, Spec: config.MarshalWatcherSpec(spec),
		Enabled: true, NextRun: &past,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	next, err := m.Activate(ctx, "svc", clockWake())
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if fp.CallCount() != 0 {
		t.Fatalf("model calls = %d, want none for an unchanged probe", fp.CallCount())
	}
	if next == nil || !next.After(time.Now()) {
		t.Fatalf("next wake = %v, want a future time", next)
	}
	w := backend.watcher(t, spec.Name)
	if w.LastRun == nil || w.LastState == "" || w.LastError != "" {
		t.Fatalf("watcher state = %+v, want last run and observation", w)
	}
	backend.wmu.Lock()
	queued := len(backend.due)
	backend.wmu.Unlock()
	if queued != 0 {
		t.Fatalf("queued events = %d, want none", queued)
	}
}

// A probe that fires appends a durable event, wakes the model exactly once, and
// acks the event it delivered.
func TestServiceProbeWakesOnChange(t *testing.T) {
	skipWithoutPOSIXShell(t)
	past := time.Now().Add(-time.Minute)
	spec := config.WatcherSpec{
		Name: "level", Kind: "command", Cmd: `printf '{"level":9}'`,
		Every: time.Minute, Timeout: 5 * time.Second,
		Trigger: &config.WatchTrigger{Kind: "changed"},
	}
	fp := &serviceTestProvider{
		replies: []string{`{"action":"done","payload":"reported","reason":"done"}`},
		done:    make(chan int, 2),
	}
	m, backend := newWatchService(t, fp, WatcherState{
		ID: "w1", SessionID: "sid-w", Name: spec.Name, Spec: config.MarshalWatcherSpec(spec),
		Enabled: true, NextRun: &past, LastState: `{"value":"old"}`,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := m.Activate(ctx, "svc", clockWake()); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if fp.CallCount() != 1 {
		t.Fatalf("model calls = %d, want 1", fp.CallCount())
	}
	backend.mu.Lock()
	appended := append([]core.Event(nil), backend.events...)
	acked := append([]int64(nil), backend.acked...)
	backend.mu.Unlock()
	if len(appended) != 1 || appended[0].DedupKey != "watch:level" {
		t.Fatalf("appended events = %+v, want one watch:level event", appended)
	}
	if len(acked) != 1 || acked[0] != appended[0].ID {
		t.Fatalf("acked = %v, want the delivered watch event %d", acked, appended[0].ID)
	}
}

// report_every is materialized as the reserved timer watcher, so the heartbeat
// shares the probes' schedule instead of being a second, drifting timer.
func TestServiceReportWatcherIsCreated(t *testing.T) {
	fp := &serviceTestProvider{
		replies: []string{`{"action":"done","payload":"ok","reason":"done"}`},
		done:    make(chan int, 2),
	}
	m, backend := newWatchService(t, fp)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wake := core.Wake{Reason: "user", Events: []core.Event{{ID: 1, Class: core.EventUser, Kind: "user_input"}}}
	next, err := m.Activate(ctx, "svc", wake)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if fp.CallCount() != 1 {
		t.Fatalf("model calls = %d, want 1", fp.CallCount())
	}
	w := backend.watcher(t, config.ReportWatcherName)
	if !w.Enabled || w.NextRun == nil || !w.NextRun.After(time.Now()) {
		t.Fatalf("report watcher = %+v, want a future next run", w)
	}
	if next == nil || !next.After(time.Now()) {
		t.Fatalf("next wake = %v, want the report watcher's time", next)
	}
}

// Pending events are recovered from the store at the head of an activation, so
// work queued before a restart is not lost.
func TestServiceRecoversDueEvents(t *testing.T) {
	fp := &serviceTestProvider{
		replies: []string{`{"action":"done","payload":"ok","reason":"done"}`},
		done:    make(chan int, 2),
	}
	m, backend := newWatchService(t, fp)
	backend.due = []core.Event{{ID: 7, Class: core.EventUser, Kind: "user_input", Source: "user", Payload: "recovered"}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := m.Activate(ctx, "svc", core.Wake{}); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if fp.CallCount() != 1 {
		t.Fatalf("model calls = %d, want the recovered event to wake the model once", fp.CallCount())
	}
	backend.mu.Lock()
	acked := append([]int64(nil), backend.acked...)
	backend.mu.Unlock()
	if len(acked) != 1 || acked[0] != 7 {
		t.Fatalf("acked = %v, want the recovered event 7", acked)
	}
}
