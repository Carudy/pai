package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
	"github.com/Carudy/pai/internal/watcher"
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

func (b *watchTestBackend) DueEvents(_ string, _ time.Time, limit int) ([]core.Event, error) {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	out := append([]core.Event(nil), b.due...)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
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
			spec:     `{"goal":"watch the river","role":"devops","workspace":"/w"}`,
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

// A due probe whose predicate is false must not spend a model call, but must
// still record what it saw and schedule the next look.
func TestServiceProbeSkipsModelWhenNothingChanged(t *testing.T) {
	skipWithoutPOSIXShell(t)
	past := time.Now().Add(-time.Minute)
	spec := config.WatcherSpec{
		Name: "level", Command: "echo 0",
		Interval: time.Minute, Timeout: 5 * time.Second,
	}
	fp := &serviceTestProvider{done: make(chan int, 1)}
	m, backend := newWatchService(t, fp, WatcherState{
		ID: "w1", SessionID: "sid-w", Name: spec.Name, Spec: config.MarshalWatcherSpec(spec),
		Enabled: true, ApprovedAt: &past, NextRun: &past,
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
		Name: "level", Command: `printf '1 level=9\n'`,
		Interval: time.Minute, Timeout: 5 * time.Second,
	}
	fp := &serviceTestProvider{
		replies: []string{`{"action":"done","payload":"reported","reason":"done"}`},
		done:    make(chan int, 2),
	}
	m, backend := newWatchService(t, fp, WatcherState{
		ID: "w1", SessionID: "sid-w", Name: spec.Name, Spec: config.MarshalWatcherSpec(spec),
		Enabled: true, ApprovedAt: &past, NextRun: &past,
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
	// The fire is closed once its wake is acknowledged; that timestamp is the
	// baseline a repeat_after reminder counts from.
	state := backend.watcher(t, spec.Name)
	if _, open := watcher.Open(state.LastState); open {
		t.Fatalf("watcher state = %s, want the handled fire to be closed", state.LastState)
	}
}

// A probe whose command is untrusted asks with the same confirmation card a chat
// tool call uses. Nothing runs before the answer; deny skips the pass, and a
// later "once" runs it without leaving a standing grant.
func TestServiceProbeAsksForApproval(t *testing.T) {
	skipWithoutPOSIXShell(t)
	past := time.Now().Add(-time.Minute)
	spec := config.WatcherSpec{
		Name: "probe", Command: "echo ran > probe-ran; echo 0",
		Interval: time.Minute, Timeout: 5 * time.Second,
	}
	fp := &serviceTestProvider{done: make(chan int, 1)}
	m, backend := newWatchService(t, fp, WatcherState{
		ID: "w1", SessionID: "sid-w", Name: spec.Name, Spec: config.MarshalWatcherSpec(spec),
		Enabled: true, NextRun: &past,
	})
	marker := filepath.Join(backend.meta.Cwd, "probe-ran")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := m.Activate(ctx, "svc", clockWake()); done <- err }()

	s := waitSnapshot(t, m, "svc", func(s Snapshot) bool { return s.Pending != nil })
	if s.Pending.TrustTarget != "command" || s.Pending.Tool == nil || s.Pending.Tool.Detail != spec.Command {
		t.Fatalf("prompt = %+v", s.Pending)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a probe ran before the operator answered")
	}
	if err := m.Reply("svc", s.Pending.ID, "", core.TrustDeny); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a denied probe ran")
	}
	if w := backend.watcher(t, spec.Name); w.LastRun != nil {
		t.Fatalf("a denied probe was recorded as run: %+v", w)
	}

	// A later "once" runs it, without recording a standing grant.
	backend.dueNow(t, spec.Name)
	done = make(chan error, 1)
	go func() { _, err := m.Activate(ctx, "svc", clockWake()); done <- err }()
	s = waitSnapshot(t, m, "svc", func(s Snapshot) bool { return s.Pending != nil })
	if err := m.Reply("svc", s.Pending.ID, "", core.TrustOnce); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Activate (approved): %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the approved probe did not run: %v", err)
	}
}

// A command name the session already trusts is a grant: the probe runs without
// asking, and no per-watcher approval is written.
func TestServiceProbeRunsWhenTheNameIsTrusted(t *testing.T) {
	skipWithoutPOSIXShell(t)
	past := time.Now().Add(-time.Minute)
	spec := config.WatcherSpec{
		Name: "probe", Command: "echo 0",
		Interval: time.Minute, Timeout: 5 * time.Second,
	}
	fp := &serviceTestProvider{done: make(chan int, 1)}
	m, backend := newWatchService(t, fp, WatcherState{
		ID: "w1", SessionID: "sid-w", Name: spec.Name, Spec: config.MarshalWatcherSpec(spec),
		Enabled: true, NextRun: &past,
	})
	if err := m.SetCommandTrust("svc", "echo", true); err != nil {
		t.Fatalf("SetCommandTrust: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := m.Activate(ctx, "svc", clockWake()); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	w := backend.watcher(t, spec.Name)
	if w.LastRun == nil {
		t.Fatal("a trusted probe did not run")
	}
	if w.ApprovedAt != nil {
		t.Fatal("a trust-based run wrote a per-watcher approval")
	}
}

// The stored goal is the service's standing instruction: it must reach the
// prompt head, not just `pai service show`.
func TestServiceGoalReachesThePromptHead(t *testing.T) {
	fp := &serviceTestProvider{
		replies: []string{`{"action":"done","payload":"ok","reason":"done"}`},
		done:    make(chan int, 2),
	}
	m, _ := newWatchService(t, fp)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wake := core.Wake{Reason: "user", Events: []core.Event{{ID: 1, Class: core.EventUser, Kind: "user_input", Payload: "go"}}}
	if _, err := m.Activate(ctx, "svc", wake); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	params := fp.LastParams()
	if len(params.Messages) == 0 {
		t.Fatal("the provider received no messages")
	}
	head := params.Messages[0].Content
	if !strings.Contains(head, "## Service goal") || !strings.Contains(head, "watch the river") {
		t.Fatalf("the goal is missing from the head:\n%s", head)
	}
}

// The situation names the watcher that fired and carries its output, so the
// model can tell a probe result apart from an instruction. The scheduler's own
// clock tick is not repeated: the probe that fired already explains the wake.
// A watcher's numeric priority travels on its event without changing the event's
// policy class.
func TestWatcherEventCarriesPriority(t *testing.T) {
	ev := watcherEvent("sid", config.WatcherSpec{Name: "gate", Command: "true", Priority: 85},
		watcher.Result{Summary: "open"}, time.Now())
	if ev.Class != core.EventWatcher || ev.Priority != 85 {
		t.Fatalf("event = %+v, want a watcher at priority 85", ev)
	}
}

func TestRenderSituationNamesWatchers(t *testing.T) {
	wake := core.Wake{Reason: "timer", Events: []core.Event{
		{Class: core.EventTimer, Kind: "timer", Source: "clock"},
		{Class: core.EventWatcher, Kind: "watch", Source: "watch:river-level", Payload: "mode=flood level=3.7"},
		{Class: core.EventTimer, Kind: "timer", Source: "watch:tick", Payload: "Report the state in one line."},
		{Class: core.EventUser, Kind: "user_input", Payload: "check the gate"},
	}}
	got := renderSituation(wake, nil)
	for _, want := range []string{
		`- watcher "river-level" fired: mode=flood level=3.7`,
		`- timer "tick" fired: Report the state in one line.`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("situation is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "check the gate") {
		t.Fatalf("a recorded user turn must not be repeated in the situation:\n%s", got)
	}
	if strings.Contains(got, "clock") {
		t.Fatalf("the scheduler's clock tick leaked into the situation:\n%s", got)
	}
}

// One activation may be woken for watcher A while watcher B is still open. The
// situation names B so the model does not restart work that is already queued.
func TestRenderSituationListsOpenItems(t *testing.T) {
	wake := core.Wake{Reason: "timer", Events: []core.Event{
		{Class: core.EventWatcher, Kind: "watch", Source: "watch:river-level", Payload: "level=3.7"},
	}}
	got := renderSituation(wake, []openItem{{Name: "gate", Fired: time.Unix(1_700_000_000, 0)}})
	for _, want := range []string{"### Open items", `- watcher "gate": open since`} {
		if !strings.Contains(got, want) {
			t.Fatalf("open-items block is missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "river-level"); n != 1 {
		// The watcher in the wake is described once, by its fired line.
		t.Fatalf("river-level appears %d times, want 1:\n%s", n, got)
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
// in status until a human (or a probe) wakes it again.
func TestServiceParksOnAsk(t *testing.T) {
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

func (b *multiServiceBackend) AckEvents(string, []int64) error { return nil }
func (b *multiServiceBackend) NackEvent(string, int64, error, time.Duration, bool) error {
	return nil
}
func (b *multiServiceBackend) DueEvents(string, time.Time, int) ([]core.Event, error) {
	return nil, nil
}
func (b *multiServiceBackend) NextEventAt(string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (b *multiServiceBackend) SetNextWake(string, *time.Time) error { return nil }

// Stopping is durable and stops scheduling. An explicit user instruction resumes
// the service; a later Stop/Start cycle works the same way.
func TestServiceStopAndStart(t *testing.T) {
	fp := &serviceTestProvider{
		replies: []string{
			`{"action":"done","payload":"ok","reason":"done"}`,
			`{"action":"done","payload":"ok again","reason":"done"}`,
		},
		done: make(chan int, 2),
	}
	m, backend := newWatchService(t, fp)

	if err := m.StopService("svc"); err != nil {
		t.Fatalf("StopService: %v", err)
	}
	state, status := backend.persistedState()
	if state != StateStopped || !strings.Contains(status, "stopped") {
		t.Fatalf("state = %q, status = %q; want stopped", state, status)
	}
	if running, queued, next := m.scheduler.Snapshot("svc"); running || queued || next != nil {
		t.Fatalf("the stopped service is still scheduled: running=%v queued=%v next=%v", running, queued, next)
	}
	// Typing to a stopped service is an explicit resume signal, not an error.
	if err := m.Send("svc", "hello"); err != nil {
		t.Fatalf("Send to a stopped service: %v", err)
	}
	if state, _ := backend.persistedState(); state != StateSuspended {
		t.Fatalf("state = %q, want suspended after a user message", state)
	}
	waitFor(t, "the resumed service to run", func() bool { return fp.CallCount() == 1 })

	// The explicit path still works after another stop.
	if err := m.StopService("svc"); err != nil {
		t.Fatalf("StopService: %v", err)
	}
	if err := m.StartService("svc"); err != nil {
		t.Fatalf("StartService: %v", err)
	}
	if err := m.Send("svc", "again"); err != nil {
		t.Fatalf("Send after start: %v", err)
	}
	waitFor(t, "the restarted service to run", func() bool { return fp.CallCount() == 2 })
}

// Stopping a parked worker must unblock it: the worker is waiting in the inbox,
// not finishing a task, and an operator stop is not a request to answer first.
func TestServiceStopCancelsAParkedWorker(t *testing.T) {
	fp := &serviceTestProvider{
		replies: []string{`{"action":"ask","payload":"which gate?","reason":"need input"}`},
		done:    make(chan int, 2),
	}
	m, _ := newWatchService(t, fp)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wake := core.Wake{Reason: "user", Events: []core.Event{{ID: 1, Class: core.EventUser, Kind: "user_input", Payload: "go"}}}
	if _, err := m.Activate(ctx, "svc", wake); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- m.StopService("svc") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StopService: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopService did not unblock a parked worker")
	}
}

// A service persisted as stopped is loaded but never registered, so a restart
// cannot silently resume it. A user instruction is still allowed to resume it.
func TestStartServicesSkipsStopped(t *testing.T) {
	fp := &serviceTestProvider{
		replies: []string{`{"action":"done","payload":"ok","reason":"done"}`},
		done:    make(chan int, 1),
	}
	backend := &watchTestBackend{
		serviceTestBackend: &serviceTestBackend{
			meta:     Meta{Name: "svc", ID: "sid", Mode: ModeService, State: StateStopped, Cwd: t.TempDir(), CreatedAt: time.Now()},
			provider: fp,
			spec:     `{"goal":"g","role":"devops","workspace":"/w"}`,
		},
	}
	m := New(context.Background(), backend, 2)
	t.Cleanup(m.Close)
	if err := m.StartServices(); err != nil {
		t.Fatal(err)
	}
	if running, queued, next := m.scheduler.Snapshot("svc"); running || queued || next != nil {
		t.Fatalf("a stopped service was registered: running=%v queued=%v next=%v", running, queued, next)
	}
	if err := m.Send("svc", "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFor(t, "the user message to resume the service", func() bool { return fp.CallCount() == 1 })
}

// Mail is recorded as a conversation turn, so the situation projection must not
// repeat it: the model would read the same message twice.
func TestRenderSituationOmitsRecordedMail(t *testing.T) {
	s := renderSituation(core.Wake{
		Reason: "mail from alice",
		Events: []core.Event{{
			Class: core.EventPeer, Kind: "mail", Source: "session:alice", Payload: "check the gate",
		}},
	}, nil)
	if strings.Contains(s, "check the gate") {
		t.Fatalf("mail repeated in the situation:\n%s", s)
	}
	if !strings.Contains(s, "mail from alice") {
		t.Fatalf("wake reason missing:\n%s", s)
	}
}

// The served worker has no terminal debug writer, so parser format failures must
// reach the operator diagnostics directly. Full model output is not forwarded;
// the bounded format line already names the reason, size, and tokens.
func TestWorkerForwardsFormatDiagnostics(t *testing.T) {
	m, _ := newWatchService(t, &serviceTestProvider{done: make(chan int, 1)})
	diags, unsubscribe, err := m.SubscribeDiagnostics()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	w := &worker{m: m, name: "svc"}
	w.Debugf("[Format Error attempt 1/3] bytes=32123 finish_reason=%q completion_tokens=8000 truncated=true", "length")
	select {
	case d := <-diags:
		if d.Kind != "model" || d.Outcome != "format error" || !strings.Contains(d.Detail, "finish_reason=\"length\"") {
			t.Fatalf("diagnostic = %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("format diagnostic was not forwarded")
	}

	w.Debugf("[AI Output]:\nsecret model text")
	select {
	case d := <-diags:
		t.Fatalf("full model output leaked into diagnostics: %+v", d)
	case <-time.After(20 * time.Millisecond):
	}
}

// A failed activation backs off instead of waking every 30s forever. The last
// attempt dead-letters the event so the service can go quiet and the operator can
// inspect it in the queue.
func TestEventRetryBacksOffThenDeadLetters(t *testing.T) {
	m, backend := newWatchService(t, &serviceTestProvider{done: make(chan int, 1)})
	w := &worker{m: m, sessionID: "sid-w"}
	wake := core.Wake{Events: []core.Event{
		{ID: 1, Source: "watch:quick", Attempts: 0},
		{ID: 2, Source: "watch:dead", Attempts: 5},
	}}
	if err := w.ackWake(wake, errors.New("provider down")); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.nackDelays) != 2 || backend.nackDelays[0] != 30*time.Second || backend.nackDelays[1] != 0 {
		t.Fatalf("delays = %v", backend.nackDelays)
	}
	if len(backend.nackDead) != 2 || backend.nackDead[0] || !backend.nackDead[1] {
		t.Fatalf("dead = %v", backend.nackDead)
	}
}

// A probe whose event cannot be queued must stay due: advancing the watcher
// state first would mark the observation as already reported and silently lose it.
func TestProbeQueueFailureKeepsWatcherDue(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	spec := config.WatcherSpec{Name: "tick", Interval: time.Minute}
	fp := &serviceTestProvider{done: make(chan int, 1)}
	backend := &watchTestBackend{
		serviceTestBackend: &serviceTestBackend{
			meta:      Meta{Name: "svc", ID: "sid-w", Mode: ModeService, Cwd: t.TempDir(), CreatedAt: time.Now()},
			provider:  fp,
			spec:      `{"goal":"g","role":"devops","workspace":"/w"}`,
			appendErr: errors.New("database is locked"),
		},
		watchers: []WatcherState{{
			ID: "w1", SessionID: "sid-w", Name: spec.Name, Spec: config.MarshalWatcherSpec(spec),
			Enabled: true, NextRun: &past,
		}},
	}
	m := New(context.Background(), backend, 2)
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	next, err := m.Activate(ctx, "svc", clockWake())
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if next == nil || next.After(time.Now()) {
		t.Fatalf("next = %v, want the probe due immediately", next)
	}
	w := backend.watcher(t, spec.Name)
	if w.LastRun != nil || w.LastState != "" {
		t.Fatalf("watcher advanced despite the queue failure: %+v", w)
	}
}

// A probe may refresh a pending event whose id is already in the recovered due
// set. The wake must carry the refreshed payload, not the stale copy.
func TestMergeEventsPrefersNewestPayload(t *testing.T) {
	old := core.Event{ID: 7, Class: core.EventWatcher, Priority: 50, Payload: "old"}
	fresh := core.Event{ID: 7, Class: core.EventWatcher, Priority: 50, Payload: "fresh"}
	got := mergeEvents([]core.Event{old}, []core.Event{fresh})
	if len(got) != 1 || got[0].Payload != "fresh" {
		t.Fatalf("merged = %+v, want the fresh payload", got)
	}
}

// A pending event that survived a restart must wake the session even when no
// timer would; otherwise mail or a queued probe result can sleep indefinitely.
func TestStartNextPrefersPendingEvents(t *testing.T) {
	backend := &watchTestBackend{
		serviceTestBackend: &serviceTestBackend{
			meta: Meta{Name: "svc", ID: "sid", Mode: ModeService, Cwd: t.TempDir(), CreatedAt: time.Now()},
		},
	}
	m := New(context.Background(), backend, 2)
	t.Cleanup(m.Close)
	now := time.Now()
	due := now.Add(-time.Second)
	backend.nextEventAt = &due
	if next := m.startNext(backend.meta, now); next == nil || next.After(now) {
		t.Fatalf("next = %v, want due now for a pending event", next)
	}
	backend.nextEventAt = nil
	future := now.Add(time.Hour)
	backend.meta.NextWake = &future
	if next := m.startNext(backend.meta, now); next == nil || !next.Equal(future) {
		t.Fatalf("next = %v, want the stored wake", next)
	}
}

// An unreadable queue is not an empty queue: startup must retry shortly rather
// than reading the error as "nothing to do" and sleeping forever.
func TestStartNextRetriesOnQueueError(t *testing.T) {
	backend := &watchTestBackend{
		serviceTestBackend: &serviceTestBackend{
			meta: Meta{Name: "svc", ID: "sid", Mode: ModeService, Cwd: t.TempDir(), CreatedAt: time.Now()},
		},
	}
	backend.nextEventErr = errors.New("database is locked")
	m := New(context.Background(), backend, 2)
	t.Cleanup(m.Close)
	now := time.Now()
	next := m.startNext(backend.meta, now)
	if next == nil {
		t.Fatal("next = nil, want a retry after a queue read error")
	}
	if next.Before(now) || next.After(now.Add(pendingRetryDelay+time.Second)) {
		t.Fatalf("next = %v, want a retry within %v", next, pendingRetryDelay)
	}
}

// The same rule after an activation: a failed queue read must schedule a retry,
// even when the activation itself found nothing substantive to do.
func TestActivationSchedulesRetryOnQueueError(t *testing.T) {
	fp := &serviceTestProvider{done: make(chan int, 1)}
	backend := &watchTestBackend{
		serviceTestBackend: &serviceTestBackend{
			meta:     Meta{Name: "svc", ID: "sid-w", Mode: ModeService, Cwd: t.TempDir(), CreatedAt: time.Now()},
			provider: fp,
			spec:     `{"goal":"g","role":"devops","workspace":"/w"}`,
		},
	}
	backend.nextEventErr = errors.New("database is locked")
	m := New(context.Background(), backend, 2)
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	next, err := m.Activate(ctx, "svc", clockWake())
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if fp.CallCount() != 0 {
		t.Fatalf("model calls = %d, want none", fp.CallCount())
	}
	if next == nil || next.Before(start) || next.After(start.Add(pendingRetryDelay+time.Second)) {
		t.Fatalf("next = %v, want a retry within %v", next, pendingRetryDelay)
	}
}

// A pending event with a future retry time (a nack) must become the next wake
// even when no watcher is due, so it cannot go idle until another message.
func TestActivationSchedulesPendingRetry(t *testing.T) {
	fp := &serviceTestProvider{done: make(chan int, 1)}
	backend := &watchTestBackend{
		serviceTestBackend: &serviceTestBackend{
			meta:     Meta{Name: "svc", ID: "sid-w", Mode: ModeService, Cwd: t.TempDir(), CreatedAt: time.Now()},
			provider: fp,
			spec:     `{"goal":"g","role":"devops","workspace":"/w"}`,
		},
	}
	retry := time.Now().Add(2 * time.Minute)
	backend.nextEventAt = &retry
	m := New(context.Background(), backend, 2)
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	next, err := m.Activate(ctx, "svc", clockWake())
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if fp.CallCount() != 0 {
		t.Fatalf("model calls = %d, want none for a bare clock tick", fp.CallCount())
	}
	if next == nil || next.Sub(retry).Abs() > time.Second {
		t.Fatalf("next = %v, want the pending retry %v", next, retry)
	}
}

// One activation carries at most maxDueEvents; a larger backlog must make the
// session due again immediately, so the queue drains instead of waiting for a
// timer.
func TestActivationDrainsBacklog(t *testing.T) {
	fp := &serviceTestProvider{
		replies: []string{`{"action":"yield","payload":{},"reason":"drained"}`},
		done:    make(chan int, 1),
	}
	backend := &watchTestBackend{
		serviceTestBackend: &serviceTestBackend{
			meta:     Meta{Name: "svc", ID: "sid-w", Mode: ModeService, Cwd: t.TempDir(), CreatedAt: time.Now()},
			provider: fp,
			spec:     `{"goal":"drain","role":"devops","workspace":"/w"}`,
		},
	}
	for i := 0; i < maxDueEvents+5; i++ {
		backend.due = append(backend.due, core.Event{
			ID: int64(i + 1), Class: core.EventWatcher, Kind: "watch", Source: "watch:x", Payload: "fired",
		})
	}
	m := New(context.Background(), backend, 2)
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	next, err := m.Activate(ctx, "svc", clockWake())
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if got := fp.CallCount(); got != 1 {
		t.Fatalf("model calls = %d, want 1", got)
	}
	if next == nil || next.After(time.Now()) {
		t.Fatalf("next = %v, want due now while a backlog remains", next)
	}
}

// A suspended service is idle even though its worker is resident, so the same
// model/role changes a chat session allows must work. The workspace is the
// exception: it is part of the manifest.
func TestServiceSettingsWhileSuspended(t *testing.T) {
	fp := &serviceTestProvider{
		replies: []string{`{"action":"done","payload":"ok","reason":"done"}`},
		done:    make(chan int, 2),
	}
	m, backend := newWatchService(t, fp)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wake := core.Wake{Reason: "user", Events: []core.Event{{ID: 1, Class: core.EventUser, Kind: "user_input", Payload: "go"}}}
	if _, err := m.Activate(ctx, "svc", wake); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	if err := m.SetModel("svc", "other:model"); err != nil {
		t.Fatalf("SetModel on a suspended service: %v", err)
	}
	if backend.meta.Model != "other:model" {
		t.Fatalf("model = %q, want other:model", backend.meta.Model)
	}
	if err := m.SetRole("svc", "coder"); err != nil {
		t.Fatalf("SetRole on a suspended service: %v", err)
	}
	if backend.meta.Role != "coder" {
		t.Fatalf("role = %q, want coder", backend.meta.Role)
	}
	if err := m.SetWorkingDir("svc", t.TempDir()); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("SetWorkingDir = %v, want a manifest error", err)
	}
}

// A user instruction takes priority over the head-of-activation probe pass: the
// reply must not wait behind commands. The overdue probe runs on the next wake.
func TestServiceUserInputSkipsProbes(t *testing.T) {
	skipWithoutPOSIXShell(t)
	past := time.Now().Add(-time.Minute)
	spec := config.WatcherSpec{
		Name: "probe", Command: "echo ran > probe-ran; echo 0",
		Interval: time.Minute, Timeout: 5 * time.Second,
	}
	fp := &serviceTestProvider{
		replies: []string{`{"action":"done","payload":"answered","reason":"done"}`},
		done:    make(chan int, 2),
	}
	m, backend := newWatchService(t, fp, WatcherState{
		ID: "w1", SessionID: "sid-w", Name: spec.Name, Spec: config.MarshalWatcherSpec(spec),
		Enabled: true, ApprovedAt: &past, NextRun: &past,
	})
	marker := filepath.Join(backend.meta.Cwd, "probe-ran")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wake := core.Wake{Reason: "user", Events: []core.Event{{ID: 1, Class: core.EventUser, Kind: "user_input", Payload: "are you there?"}}}
	if _, err := m.Activate(ctx, "svc", wake); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if fp.CallCount() != 1 {
		t.Fatalf("model calls = %d, want the user instruction answered", fp.CallCount())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a due probe ran before the user instruction")
	}

	// The next activation has no user input, so the overdue probe runs.
	if _, err := m.Activate(ctx, "svc", clockWake()); err != nil {
		t.Fatalf("Activate (probe): %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the overdue probe did not run afterwards: %v", err)
	}
}

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
