package runner

import (
	"context"
	"testing"

	"github.com/Carudy/pai/internal/core"
)

func TestUsageSnapshots(t *testing.T) {
	m := New(context.Background(), &fakeBackend{}, 1)
	defer m.Close()
	w := &worker{m: m, name: "one", subs: make(map[chan Event]struct{})}
	m.entries["one"] = w
	initial, _ := m.Snapshot("one")
	if initial.UsageCalls != 0 {
		t.Fatal(initial)
	}
	first := core.Usage{Prompt: 10, Completion: 2, Total: 12}
	second := core.Usage{Prompt: 20, Completion: 3, Total: 23}
	w.Usage(first)
	old, _ := m.Snapshot("one")
	w.Usage(second)
	if old.Usage != first || old.TotalUsage != first || old.UsageCalls != 1 {
		t.Fatalf("previous snapshot changed: %+v", old)
	}
	want := core.Usage{Prompt: 30, Completion: 5, Total: 35}
	for i := 0; i < 2; i++ {
		ch, unsubscribe, err := m.Subscribe("one")
		if err != nil {
			t.Fatal(err)
		}
		s := (<-ch).Snapshot
		unsubscribe()
		if s.Usage != second || s.TotalUsage != want || s.UsageCalls != 2 {
			t.Fatalf("reconnect: %+v", s)
		}
	}
	old.Usage.Prompt = 999
	old.TotalUsage.Total = 999
	s, _ := m.Snapshot("one")
	if s.TotalUsage != want || s.Usage != second {
		t.Fatal("snapshot aliases worker")
	}
	// Replacing a retired worker starts a fresh runtime, not session-wide totals.
	m.mu.Lock()
	m.entries["one"] = &worker{m: m, name: "one"}
	m.mu.Unlock()
	s, _ = m.Snapshot("one")
	if s.UsageCalls != 0 || s.TotalUsage != (core.Usage{}) {
		t.Fatal(s)
	}
	// These synthetic workers have no goroutine to join on Close.
	m.mu.Lock()
	delete(m.entries, "one")
	m.mu.Unlock()
}
