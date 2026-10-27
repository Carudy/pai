package session

import (
	"time"

	"github.com/Carudy/pai/internal/core"
)

// ServiceInfo is the durable service state a scheduler or CLI needs. It is
// separate from Meta so listing chat sessions does not load a service manifest
// or status document.
type ServiceInfo struct {
	Meta
	Status string `json:"status,omitempty"`
	Spec   string `json:"spec,omitempty"`
}

// Watcher is one host-side periodic check attached to a service session. Spec
// is the manifest JSON (type, interval, hold_for, repeat_after); LastState is the
// predicate's persisted window/latch state, so a wake happens only when the
// condition stays true long enough and the episode has not already fired.
type Watcher struct {
	ID         string
	SessionID  string
	Name       string
	Spec       string
	Enabled    bool
	ApprovedAt *time.Time
	LastRun    *time.Time
	NextRun    *time.Time
	LastState  string
	LastError  string
	Version    int
}

// ServiceStore is the durable half of the resident-session runtime. SQLite is
// the only implementation; callers type-assert a Store to it. Events and
// watchers reference sessions by immutable ID, never by display name.
type ServiceStore interface {
	// ServiceInfo returns one service's metadata, status, and manifest spec.
	ServiceInfo(name string) (*ServiceInfo, error)
	// SetServiceState persists the runtime state and bounded status document.
	// Neither bumps updated_at: a service waking on a timer must not reorder the
	// session list on every probe.
	SetServiceState(name, state, status string) error
	// SetNextWake records when the scheduler should wake the service; nil clears it.
	SetNextWake(name string, at *time.Time) error
	// SetServiceSpec stores the validated manifest JSON for a service.
	SetServiceSpec(name, spec string) error

	// AppendEvent enqueues an event. With a DedupKey, an existing pending event
	// for the same session and key is replaced and its id returned.
	AppendEvent(ev core.Event) (int64, error)
	// DueEvents returns pending events whose At has arrived, most urgent first.
	DueEvents(sessionID string, now time.Time, limit int) ([]core.Event, error)
	// LeaseEvents marks events in flight, so a crash can distinguish them from
	// pending work and reset them on the next open.
	LeaseEvents(ids []int64, until time.Time) error
	// AckEvents marks delivered events done.
	AckEvents(ids []int64) error
	// NackEvent returns an event to pending (or moves it to dead) with a retry
	// time and the failure reason.
	NackEvent(id int64, retryAt time.Time, lastErr string, dead bool) error
	// PendingEvents counts pending and leased events for a snapshot.
	PendingEvents(sessionID string) (int, error)
	// ResetLeases returns in-flight events to pending; no worker survives a process.
	ResetLeases() error

	// PutWatcher inserts or replaces a watcher by (session_id, name).
	PutWatcher(w Watcher) error
	Watchers(sessionID string) ([]Watcher, error)
	SetWatcherState(id string, lastRun, nextRun *time.Time, lastState, lastErr string) error
	DeleteWatcher(id string) error
}

var _ ServiceStore = (*sqliteStore)(nil)
