package runner

import (
	"context"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/core"
)

type schedulerCall struct {
	name string
	wake core.Wake
}

type fakeSchedulerHost struct {
	calls   chan schedulerCall
	release chan struct{}
	next    map[string]*time.Time
}

func newFakeSchedulerHost() *fakeSchedulerHost {
	return &fakeSchedulerHost{
		calls: make(chan schedulerCall, 8), release: make(chan struct{}, 8),
		next: make(map[string]*time.Time),
	}
}

func (h *fakeSchedulerHost) Activate(ctx context.Context, name string, wake core.Wake) (*time.Time, error) {
	h.calls <- schedulerCall{name: name, wake: wake}
	select {
	case <-h.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return h.next[name], nil
}

func receiveCall(t *testing.T, ch <-chan schedulerCall) schedulerCall {
	t.Helper()
	select {
	case call := <-ch:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an activation")
		return schedulerCall{}
	}
}

func waitIdle(t *testing.T, s *Scheduler, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		running, queued, _ := s.Snapshot(name)
		if !running && !queued {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s did not become idle", name)
}

// A wake starts an activation; a second wake arriving while it runs is queued
// for the next activation rather than starting a concurrent one. The running
// wake is not re-delivered.
func TestSchedulerQueuesWakeDuringActivation(t *testing.T) {
	host := newFakeSchedulerHost()
	s := NewScheduler(context.Background(), host, 2)
	defer s.Close()
	s.Register("a", nil)

	if !s.Wake("a", core.Wake{
		Reason: "user",
		Events: []core.Event{{ID: 1, Class: core.EventUser, Kind: "user"}},
	}) {
		t.Fatal("registered service rejected a wake")
	}
	first := receiveCall(t, host.calls)
	if first.name != "a" || first.wake.Reason != "user" || len(first.wake.Events) != 1 {
		t.Fatalf("first call = %+v", first)
	}

	if !s.Wake("a", core.Wake{
		Reason: "mail",
		Events: []core.Event{{ID: 2, Class: core.EventPeer, Kind: "mail"}},
	}) {
		t.Fatal("wake during activation was rejected")
	}
	host.release <- struct{}{}
	second := receiveCall(t, host.calls)
	if second.wake.Reason != "mail" || len(second.wake.Events) != 1 || second.wake.Events[0].ID != 2 {
		t.Fatalf("pending wake = %+v, want only the event that arrived while running", second.wake)
	}
	host.release <- struct{}{}
	waitIdle(t, s, "a")
}

// Capacity bounds concurrent activations; a queued service runs when a slot
// frees, and an overdue timer is delivered as a timer event.
func TestSchedulerCapacityAndTimer(t *testing.T) {
	host := newFakeSchedulerHost()
	s := NewScheduler(context.Background(), host, 1)
	defer s.Close()

	next := time.Now().Add(20 * time.Millisecond)
	s.Register("timer", &next)
	timerCall := receiveCall(t, host.calls)
	if timerCall.name != "timer" || len(timerCall.wake.Events) != 1 || timerCall.wake.Events[0].Class != core.EventTimer {
		t.Fatalf("timer call = %+v", timerCall)
	}

	s.Register("other", nil)
	s.Wake("other", core.Wake{
		Reason: "user",
		Events: []core.Event{{ID: 9, Class: core.EventUser, Kind: "user"}},
	})
	select {
	case call := <-host.calls:
		t.Fatalf("%s started while capacity was full: %+v", call.name, call)
	case <-time.After(30 * time.Millisecond):
	}

	host.release <- struct{}{}
	other := receiveCall(t, host.calls)
	if other.name != "other" {
		t.Fatalf("next call = %+v, want other", other)
	}
	host.release <- struct{}{}
	waitIdle(t, s, "other")
}

func TestSchedulerSetNextAndUnregister(t *testing.T) {
	host := newFakeSchedulerHost()
	s := NewScheduler(context.Background(), host, 1)
	defer s.Close()

	s.Register("svc", nil)
	next := time.Now().Add(10 * time.Millisecond)
	s.SetNext("svc", &next)
	call := receiveCall(t, host.calls)
	if call.name != "svc" || len(call.wake.Events) != 1 || call.wake.Events[0].Class != core.EventTimer {
		t.Fatalf("call = %+v", call)
	}
	host.release <- struct{}{}
	waitIdle(t, s, "svc")

	s.Unregister("svc")
	if s.Wake("svc", core.Wake{Reason: "late"}) {
		t.Fatal("an unregistered service accepted a wake")
	}
}

func TestMergeWakeCoalescesAndOrders(t *testing.T) {
	now := time.Now()
	a := &core.Wake{Events: []core.Event{
		{ID: 1, Class: core.EventWatcher, DedupKey: "watch:river", At: now},
		{ID: 2, Class: core.EventTimer, At: now},
	}}
	b := core.Wake{
		Reason: "newer",
		Events: []core.Event{
			{ID: 3, Class: core.EventWatcher, DedupKey: "watch:river", At: now.Add(time.Second)},
			{ID: 4, Class: core.EventUser, At: now},
		},
	}
	got := mergeWake(a, b)
	if got.Reason != "newer" || len(got.Events) != 3 {
		t.Fatalf("merged = %+v", got)
	}
	// user first, then the newer watcher, then the timer; the older watcher was
	// replaced by its dedupe key.
	if got.Events[0].ID != 4 || got.Events[1].ID != 3 || got.Events[2].ID != 2 {
		t.Fatalf("merged order = %+v", got.Events)
	}
}

func TestServiceInboxPushNextPollDone(t *testing.T) {
	ctx := context.Background()
	var acked core.Wake
	var ackResult core.WakeResult
	inbox := NewServiceInbox(1, func(w core.Wake, res core.WakeResult) error {
		acked, ackResult = w, res
		return nil
	})
	if !inbox.Push(core.Wake{Reason: "one"}) {
		t.Fatal("first push was refused")
	}
	if inbox.Push(core.Wake{Reason: "two"}) {
		t.Fatal("a second wake was queued instead of merged")
	}
	if w, ok := inbox.Poll(); !ok || w.Reason != "one" {
		t.Fatalf("Poll = (%+v, %v)", w, ok)
	}
	if _, ok := inbox.Poll(); ok {
		t.Fatal("Poll returned a wake twice")
	}

	inbox.Push(core.Wake{Reason: "next"})
	w, err := inbox.Next(ctx)
	if err != nil || w.Reason != "next" {
		t.Fatalf("Next = (%+v, %v)", w, err)
	}
	if err := inbox.Done(w, core.WakeResult{Outcome: "error", Err: context.Canceled}); err != nil {
		t.Fatal(err)
	}
	if acked.Reason != "next" || ackResult.Err != context.Canceled || ackResult.Outcome != "error" {
		t.Fatalf("ack = (%+v, %+v)", acked, ackResult)
	}
}

func TestServiceInboxNextHonorsContext(t *testing.T) {
	inbox := NewServiceInbox(1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inbox.Next(ctx); err != context.Canceled {
		t.Fatalf("Next error = %v, want context.Canceled", err)
	}
}
