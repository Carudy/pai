package runner

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
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

// dueNow makes a stored watcher due, so the next activation probes it.
func (b *watchTestBackend) dueNow(t *testing.T, name string) {
	t.Helper()
	past := time.Now().Add(-time.Second)
	b.wmu.Lock()
	defer b.wmu.Unlock()
	for i := range b.watchers {
		if b.watchers[i].Name == name {
			b.watchers[i].NextRun = &past
			return
		}
	}
	t.Fatalf("watcher %q was not stored", name)
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

// An activation that ends by asking parks the session: the question is visible
// in status, and the report heartbeat stops until a human (or a probe) wakes it.
func TestServiceParksOnAskAndSuppressesHeartbeat(t *testing.T) {
	fp := &serviceTestProvider{
		replies: []string{`{"action":"ask","payload":"which gate should open?","reason":"need input"}`},
		done:    make(chan int, 2),
	}
	m, backend := newWatchService(t, fp)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wake := core.Wake{Reason: "user", Events: []core.Event{{ID: 1, Class: core.EventUser, Kind: "user_input", Payload: "go"}}}
	if _, err := m.Activate(ctx, "svc", wake); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	state, status := backend.persistedState()
	if state != StateParked || !strings.Contains(status, "which gate") {
		t.Fatalf("state = %q, status = %q; want parked with the question", state, status)
	}

	// The report tick is due, but a heartbeat would only repeat the question.
	backend.dueNow(t, config.ReportWatcherName)
	if _, err := m.Activate(ctx, "svc", clockWake()); err != nil {
		t.Fatalf("Activate after ask: %v", err)
	}
	if fp.CallCount() != 1 {
		t.Fatalf("model calls = %d, want the parked service to skip the heartbeat", fp.CallCount())
	}
	if state, _ := backend.persistedState(); state != StateParked {
		t.Fatalf("state = %q, want it to stay parked", state)
	}
}

// An inform is the service's status document: it is persisted with the wake's
// outcome so the CLI can show the last report without replaying the transcript.
func TestServicePersistsReportAsStatus(t *testing.T) {
	fp := &serviceTestProvider{
		replies: []string{`{"action":"inform","payload":"level 3.8 m, above the warning band","reason":"threshold"}`},
		done:    make(chan int, 2),
	}
	m, backend := newWatchService(t, fp)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wake := core.Wake{Reason: "user", Events: []core.Event{{ID: 1, Class: core.EventUser, Kind: "user_input", Payload: "go"}}}
	if _, err := m.Activate(ctx, "svc", wake); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	state, status := backend.persistedState()
	if state != StateSuspended {
		t.Fatalf("state = %q, want suspended after a report", state)
	}
	if !strings.Contains(status, "level 3.8") {
		t.Fatalf("status = %q, want the report payload", status)
	}
}

// multiServiceBackend serves several resident sessions from one process, which
// the eviction test needs.
type multiServiceBackend struct {
	mu       sync.Mutex
	metas    []Meta
	provider provider.Provider
	events   int
}

func (b *multiServiceBackend) List() ([]Meta, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Meta(nil), b.metas...), nil
}

func (b *multiServiceBackend) metaFor(name string) Meta {
	for _, m := range b.metas {
		if m.Name == name {
			return m
		}
	}
	return Meta{Name: name}
}

func (b *multiServiceBackend) History(name string, _, _ int) (History, error) {
	return History{Meta: b.metaFor(name)}, nil
}

func (b *multiServiceBackend) Prepare(name string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), error) {
	cfg, p, rec, history, cleanup, _, err := b.PrepareWorkspace(name, "")
	return cfg, p, rec, history, cleanup, err
}

func (b *multiServiceBackend) PrepareWorkspace(name, _ string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), Meta, error) {
	cfg := &config.UserConfig{
		DefaultRole:  "devops",
		DefaultModel: "test:model",
		Context:      config.ContextConfig{KeepTurns: 4, ExecLimit: 8000, HeadLines: 80, TailLines: 40},
	}
	return cfg, b.provider, nil, nil, nil, b.metaFor(name), nil
}

func (b *multiServiceBackend) AppendEvent(_ string, _ core.Event) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events++
	return int64(b.events), nil
}

func (b *multiServiceBackend) AckEvents(string, []int64) error      { return nil }
func (b *multiServiceBackend) NackEvent(string, int64, error) error { return nil }
func (b *multiServiceBackend) DueEvents(string, time.Time, int) ([]core.Event, error) {
	return nil, nil
}
func (b *multiServiceBackend) SetNextWake(string, *time.Time) error { return nil }

// The resident bound retires the least recently used idle worker, and a later
// wake re-prepares it: eviction costs a runtime, never a conversation.
func TestServiceEvictsLeastRecentlyUsedResident(t *testing.T) {
	dir := t.TempDir()
	fp := &serviceTestProvider{
		replies: []string{
			`{"action":"done","payload":"one","reason":"done"}`,
			`{"action":"done","payload":"two","reason":"done"}`,
			`{"action":"done","payload":"one again","reason":"done"}`,
		},
		done: make(chan int, 4),
	}
	backend := &multiServiceBackend{
		metas: []Meta{
			{Name: "one", ID: "sid-1", Mode: ModeService, Cwd: dir, CreatedAt: time.Now()},
			{Name: "two", ID: "sid-2", Mode: ModeService, Cwd: dir, CreatedAt: time.Now()},
		},
		provider: fp,
	}
	m := New(context.Background(), backend, 2)
	m.SetMaxResident(1)
	t.Cleanup(m.Close)
	if err := m.StartServices(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	userWake := core.Wake{Reason: "user", Events: []core.Event{{ID: 1, Class: core.EventUser, Kind: "user_input"}}}

	if _, err := m.Activate(ctx, "one", userWake); err != nil {
		t.Fatalf("activate one: %v", err)
	}
	if _, err := m.Activate(ctx, "two", userWake); err != nil {
		t.Fatalf("activate two: %v", err)
	}
	waitFor(t, "the first worker to be retired", func() bool {
		_, err := m.Snapshot("one")
		return errors.Is(err, ErrNotFound)
	})

	if _, err := m.Activate(ctx, "one", userWake); err != nil {
		t.Fatalf("activate one again: %v", err)
	}
	if fp.CallCount() != 3 {
		t.Fatalf("model calls = %d, want the re-prepared worker to run", fp.CallCount())
	}
	waitFor(t, "the second worker to be retired", func() bool {
		_, err := m.Snapshot("two")
		return errors.Is(err, ErrNotFound)
	})
}
