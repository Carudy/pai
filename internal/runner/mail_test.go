package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/core"
)

func (b *serviceTestBackend) eventsSnapshot() []core.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]core.Event(nil), b.events...)
}

func mailManager(t *testing.T, fp *serviceTestProvider) (*Manager, *watchTestBackend) {
	t.Helper()
	backend := &watchTestBackend{
		serviceTestBackend: &serviceTestBackend{
			meta:     Meta{Name: "svc", ID: "sid", Mode: ModeService, Cwd: t.TempDir(), CreatedAt: time.Now()},
			provider: fp,
		},
	}
	m := New(context.Background(), backend, 2)
	t.Cleanup(m.Close)
	m.mu.Lock()
	m.serviceNames["svc"] = true
	m.sessionIDs["svc"] = "sid"
	m.mu.Unlock()
	return m, backend
}

// Mail is durable before the wake, and the event carries the sender plus the
// chain it belongs to, so the receiver can record it as peer mail.
func TestDeliverMailQueuesAndWakes(t *testing.T) {
	fp := &serviceTestProvider{
		replies: []string{`{"action":"yield","payload":{},"reason":"read"}`},
		done:    make(chan int, 4),
	}
	m, backend := mailManager(t, fp)
	if _, err := m.deliverMail("alice", "svc", "check the gate", core.MailMeta{}); err != nil {
		t.Fatalf("deliverMail: %v", err)
	}
	events := backend.eventsSnapshot()
	if len(events) != 1 {
		t.Fatalf("queued %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Class != core.EventPeer || ev.Kind != "mail" || ev.Source != "session:alice" || ev.Payload != "check the gate" || ev.Session != "sid" {
		t.Fatalf("event = %+v", ev)
	}
	// A message with no trace of its own starts one, and its first delivery is
	// hop 1.
	if ev.Trace == "" || ev.Hops != 1 {
		t.Fatalf("event trace/hops = %q/%d, want a new trace at hop 1", ev.Trace, ev.Hops)
	}
	select {
	case <-fp.done:
	case <-time.After(5 * time.Second):
		t.Fatal("mail did not wake the target")
	}
}

// A reply continues the sender's chain instead of starting a new one.
func TestDeliverMailCarriesTheChain(t *testing.T) {
	fp := &serviceTestProvider{done: make(chan int, 4)}
	m, backend := mailManager(t, fp)
	if _, err := m.deliverMail("alice", "svc", "re", core.MailMeta{Trace: "tr1", Hops: 2}); err != nil {
		t.Fatalf("deliverMail: %v", err)
	}
	ev := backend.eventsSnapshot()[0]
	if ev.Trace != "tr1" || ev.Hops != 2 {
		t.Fatalf("event = %+v, want trace tr1 at hop 2", ev)
	}
}

// Past the hop limit the message is refused before it is queued: a chain that
// long is a loop, and a polite sentence in the prompt is not a bound.
func TestDeliverMailStopsALongChain(t *testing.T) {
	fp := &serviceTestProvider{done: make(chan int, 4)}
	m, backend := mailManager(t, fp)
	_, err := m.deliverMail("alice", "svc", "re", core.MailMeta{Trace: "tr1", Hops: core.MaxMailHops + 1})
	if err == nil || !strings.Contains(err.Error(), "limit is 6") {
		t.Fatalf("deliverMail = %v, want a hop-limit refusal", err)
	}
	if got := len(backend.eventsSnapshot()); got != 0 {
		t.Fatalf("queued %d events, want none", got)
	}
}

func TestDeliverMailRejects(t *testing.T) {
	fp := &serviceTestProvider{done: make(chan int, 1)}
	m, backend := mailManager(t, fp)
	for _, tc := range []struct {
		from, to, text, want string
	}{
		{"alice", "missing", "hi", "not a resident"},
		{"alice", "alice", "hi", "same session"},
		{"alice", "svc", "  ", "empty"},
		{"alice", "svc", strings.Repeat("x", maxMailBytes+1), "longer"},
	} {
		if _, err := m.deliverMail(tc.from, tc.to, tc.text, core.MailMeta{}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("deliverMail(%+v) = %v, want %q", tc, err, tc.want)
		}
	}

	// A mailbox full of due mail is refused before another event is queued.
	backend.due = make([]core.Event, 0, maxMailPending+1)
	for i := 0; i < maxMailPending+1; i++ {
		backend.due = append(backend.due, core.Event{ID: int64(i + 1), Class: core.EventPeer, Kind: "mail"})
	}
	if _, err := m.deliverMail("alice", "svc", "hi", core.MailMeta{}); err == nil || !strings.Contains(err.Error(), "full") {
		t.Fatalf("full mailbox: %v", err)
	}
}

// A stopped target keeps the mail, but is not woken and not registered: starting
// the service is the operator's decision.
func TestDeliverMailKeepsStoppedTargetAsleep(t *testing.T) {
	fp := &serviceTestProvider{done: make(chan int, 1)}
	m, backend := mailManager(t, fp)
	m.mu.Lock()
	m.stoppedServices["svc"] = true
	m.mu.Unlock()
	if _, err := m.deliverMail("alice", "svc", "later", core.MailMeta{}); err != nil {
		t.Fatalf("deliverMail: %v", err)
	}
	if got := len(backend.eventsSnapshot()); got != 1 {
		t.Fatalf("queued %d events, want 1", got)
	}
	if running, queued, next := m.scheduler.Snapshot("svc"); running || queued || next != nil {
		t.Fatalf("a stopped target was scheduled: running=%v queued=%v next=%v", running, queued, next)
	}
}
