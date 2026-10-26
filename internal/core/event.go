package core

import (
	"context"
	"sort"
	"time"
)

// EventClass is the scheduler's priority band. Lower values are more urgent, so
// ordering can compare the class directly. A class is not enough on its own:
// within a class the deadline and arrival time break ties.
type EventClass uint8

const (
	// EventSafety is a hard interlock or abort. It must be handled
	// deterministically by the host; the model is notified afterwards, never
	// asked to decide.
	EventSafety EventClass = iota
	// EventAbort cancels the current work at the next safe boundary.
	EventAbort
	// EventUser is an instruction, steering message, or answer.
	EventUser
	// EventPeer is mail from another session. It may wake an idle session but
	// never grants trust or widens the capability set.
	EventPeer
	// EventWatcher is a host probe whose condition changed.
	EventWatcher
	// EventTimer is a scheduled wake. It is batched and only fires when nothing
	// more urgent is pending.
	EventTimer
	// EventStatus updates the situation but never wakes the model on its own.
	EventStatus
	// EventTelemetry is a raw sample. It never wakes, never enters mail, and
	// never enters the model's context.
	EventTelemetry
)

// String names the class for logs and diagnostics.
func (c EventClass) String() string {
	switch c {
	case EventSafety:
		return "safety"
	case EventAbort:
		return "abort"
	case EventUser:
		return "user"
	case EventPeer:
		return "peer"
	case EventWatcher:
		return "watcher"
	case EventTimer:
		return "timer"
	case EventStatus:
		return "status"
	case EventTelemetry:
		return "telemetry"
	default:
		return "unknown"
	}
}

// Event is one durable input to a session. Payload is bounded by the store; the
// loop never treats it as an instruction on its own. DedupeKey is the coalescing
// identity (for example "watch:river-level" or "mail:<id>").
type Event struct {
	ID       int64
	Session  string // immutable session id, not the display name
	Class    EventClass
	Kind     string // "user_input", "mail", "watch", "timer", "approval", ...
	Source   string // "user", "session:<id>", "watch:<name>", "clock"
	Payload  string // JSON or text; bounded by the store
	DedupKey string
	At       time.Time
	Deadline time.Time // zero means no deadline
	Attempts int
}

// Wake is what one activation receives: the events that caused it plus a
// bounded, host-rendered situation block. Situation is a projection, not a
// stored turn, so it never grows the transcript.
type Wake struct {
	Events    []Event
	Situation string
	Reason    string
	Preempt   bool
}

// WakeResult is how one activation ended, reported with the acknowledgment so a
// host can persist the session's state and a bounded status line. Outcome is the
// driver's terminal name — yield, done, ask, terminate, or error — and Status is
// the text of the terminal action (empty for a silent yield).
type WakeResult struct {
	Outcome string
	Status  string
	Err     error
}

// Inbox is the service driver's input. Next blocks for the next wake, Poll is
// for a step-boundary check, and Done acks on success or nacks on failure.
type Inbox interface {
	Next(ctx context.Context) (Wake, error)
	Poll() (Wake, bool)
	Done(w Wake, res WakeResult) error
}

// SortEvents orders events in place: class first, then deadline (zero last),
// then arrival time, then id. The sort is stable, so events that compare equal
// keep their input order.
func SortEvents(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.Class != b.Class {
			return a.Class < b.Class
		}
		if !a.Deadline.Equal(b.Deadline) {
			if a.Deadline.IsZero() {
				return false
			}
			if b.Deadline.IsZero() {
				return true
			}
			return a.Deadline.Before(b.Deadline)
		}
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		return a.ID < b.ID
	})
}

// CoalesceEvents keeps one event per non-empty DedupKey. The survivor is the
// newer event, unless one carries a more urgent class: a safety event must not
// be discarded because a status event with the same key arrived later. Events
// without a key are all kept, in input order.
func CoalesceEvents(events []Event) []Event {
	out := make([]Event, 0, len(events))
	index := make(map[string]int, len(events))
	for _, e := range events {
		if e.DedupKey == "" {
			out = append(out, e)
			continue
		}
		i, seen := index[e.DedupKey]
		if !seen {
			index[e.DedupKey] = len(out)
			out = append(out, e)
			continue
		}
		if supersedes(e, out[i]) {
			out[i] = e
		}
	}
	return out
}

// supersedes reports whether a should replace b for the same dedupe key.
func supersedes(a, b Event) bool {
	if a.Class != b.Class {
		return a.Class < b.Class
	}
	if !a.At.Equal(b.At) {
		return a.At.After(b.At)
	}
	return a.ID > b.ID
}

// PrepareEvents coalesces duplicates and sorts the result. Callers use one
// helper so the scheduler, tests, and any future transport agree on wake order.
func PrepareEvents(events []Event) []Event {
	out := CoalesceEvents(events)
	SortEvents(out)
	return out
}
