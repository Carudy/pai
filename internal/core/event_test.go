package core

import (
	"testing"
	"time"
)

func event(id int64, class EventClass, key string, at time.Time, deadline time.Time) Event {
	return Event{ID: id, Class: class, DedupKey: key, At: at, Deadline: deadline, Kind: "test"}
}

func ids(events []Event) []int64 {
	out := make([]int64, len(events))
	for i, e := range events {
		out[i] = e.ID
	}
	return out
}

func sameIDs(got []Event, want ...int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i].ID != want[i] {
			return false
		}
	}
	return true
}

func TestSortEventsOrdersByClassThenDeadlineThenAge(t *testing.T) {
	base := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	events := []Event{
		event(1, EventTimer, "", base.Add(time.Second), time.Time{}),
		event(2, EventUser, "", base, base.Add(time.Minute)),
		event(3, EventSafety, "", base.Add(time.Second), time.Time{}),
		event(4, EventUser, "", base, base),
		event(5, EventUser, "", base.Add(-time.Second), base),
		event(6, EventUser, "", base, base),
	}
	SortEvents(events)
	if !sameIDs(events, 3, 5, 4, 6, 2, 1) {
		t.Fatalf("order = %v; want safety, then user by deadline then age/id, then timer", ids(events))
	}
}

func TestSortEventsUsesNumericPriority(t *testing.T) {
	// Priority, not class, is the ordering axis: an urgent watcher can outrank a
	// user event, and a zero priority falls back to its class band.
	events := []Event{
		{ID: 1, Class: EventUser, Priority: 80},
		{ID: 2, Class: EventWatcher, Priority: 90},
		{ID: 3, Class: EventPeer},
	}
	SortEvents(events)
	if !sameIDs(events, 2, 1, 3) {
		t.Fatalf("order = %v; want the explicit 90, then user 80, then peer default", ids(events))
	}
}

func TestDefaultPriorities(t *testing.T) {
	for class, want := range map[EventClass]int{
		EventSafety: PrioritySafety, EventAbort: PriorityAbort, EventUser: PriorityUser,
		EventPeer: PriorityPeer, EventWatcher: PriorityWatcher, EventTimer: PriorityTimer,
		EventStatus: PriorityStatus, EventTelemetry: PriorityTelemetry,
	} {
		if got := class.DefaultPriority(); got != want {
			t.Errorf("%s default = %d, want %d", class, got, want)
		}
	}
}

func TestSortEventsKeepsZeroDeadlineLastWithinAClass(t *testing.T) {
	base := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	events := []Event{
		event(1, EventPeer, "", base, time.Time{}),
		event(2, EventPeer, "", base, base.Add(time.Hour)),
	}
	SortEvents(events)
	if !sameIDs(events, 2, 1) {
		t.Fatalf("order = %v; a deadline must sort before no deadline", ids(events))
	}
}

func TestCoalesceEventsKeepsNewestPerKey(t *testing.T) {
	base := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	events := []Event{
		event(1, EventWatcher, "watch:river", base, time.Time{}),
		event(2, EventWatcher, "watch:river", base.Add(time.Minute), time.Time{}),
		event(3, EventUser, "", base, time.Time{}),
		event(4, EventUser, "", base, time.Time{}),
	}
	got := CoalesceEvents(events)
	if !sameIDs(got, 2, 3, 4) {
		t.Fatalf("coalesced = %v; want the newest for the key and both keyless events", ids(got))
	}
}

// A later status event must not hide an earlier safety event for the same key.
func TestCoalesceEventsKeepsTheMoreUrgentClass(t *testing.T) {
	base := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	events := []Event{
		event(1, EventSafety, "watch:gate", base, time.Time{}),
		event(2, EventStatus, "watch:gate", base.Add(time.Minute), time.Time{}),
	}
	got := CoalesceEvents(events)
	if !sameIDs(got, 1) {
		t.Fatalf("coalesced = %v; the safety event was lost", ids(got))
	}
}

func TestPrepareEventsCoalescesThenSorts(t *testing.T) {
	base := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	events := []Event{
		event(1, EventTimer, "timer:slot", base, time.Time{}),
		event(2, EventUser, "", base, time.Time{}),
		event(3, EventTimer, "timer:slot", base.Add(time.Second), time.Time{}),
	}
	got := PrepareEvents(events)
	if !sameIDs(got, 2, 3) {
		t.Fatalf("prepared = %v; want the user event then the coalesced timer", ids(got))
	}
}

func TestEventClassNames(t *testing.T) {
	cases := map[EventClass]string{
		EventSafety: "safety", EventAbort: "abort", EventUser: "user",
		EventPeer: "peer", EventWatcher: "watcher", EventTimer: "timer",
		EventStatus: "status", EventTelemetry: "telemetry",
	}
	for class, want := range cases {
		if got := class.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", class, got, want)
		}
	}
	if got := EventClass(99).String(); got != "unknown" {
		t.Errorf("unknown class = %q", got)
	}
}
