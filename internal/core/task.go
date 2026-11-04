package core

import "time"

// Tasks is the group board as the agent sees it. The host binds the caller and
// resolves its group, so the model names a task id and never a session: ownership
// and the group edge are checked where they are known, not in the payload.
type Tasks interface {
	Add(title, detail string, priority int) (Task, error)
	Claim(id string) (Task, error)
	Update(id string, expectVersion int, status TaskStatus, note string) (Task, error)
	List(status TaskStatus, limit int) ([]Task, error)
}

// TaskStatus is the board's state machine. A task is claimed by exactly one
// member at a time; the lease is what makes an abandoned claim recoverable
// without asking anyone.
type TaskStatus string

const (
	// TaskOpen is unowned work.
	TaskOpen TaskStatus = "open"
	// TaskClaimed has an owner and a live lease.
	TaskClaimed TaskStatus = "claimed"
	// TaskBlocked keeps its owner but records that progress waits on something.
	TaskBlocked TaskStatus = "blocked"
	// TaskOrphaned lost its owner: the lease expired or the owner went away.
	// Anyone in the group may claim it.
	TaskOrphaned TaskStatus = "orphaned"
	// TaskDone and TaskCancelled are terminal.
	TaskDone      TaskStatus = "done"
	TaskCancelled TaskStatus = "cancelled"
)

// Board limits. They bound both the stored text and what a tool observation can
// cost the model.
const (
	MaxTaskTitle  = 200
	MaxTaskDetail = 4 << 10
	MaxTaskNote   = 1 << 10
	// DefaultTaskLease is how long a claim survives without a renew. A lease is
	// long enough for a normal activation, short enough that a dead worker's task
	// does not sit idle for an hour.
	DefaultTaskLease = 15 * time.Minute
	// BlockedEscalationAfter is how long a blocked task waits before the host
	// tells the group's lead once.
	BlockedEscalationAfter = 30 * time.Minute
)

// Task is one unit of shared work in a group. Version is optimistic
// concurrency: a write carries the version it read, and a mismatch is rejected
// rather than merged, so two members cannot silently overwrite each other.
type Task struct {
	ID        string     `json:"id"`
	GroupID   string     `json:"group_id"`
	Title     string     `json:"title"`
	Detail    string     `json:"detail,omitempty"`
	Status    TaskStatus `json:"status"`
	OwnerID   string     `json:"owner_id,omitempty"`
	OwnerName string     `json:"owner_name,omitempty"`
	Version   int        `json:"version"`
	// LeaseUntil is when an un-renewed claim becomes orphaned; zero means the task
	// has no owner. The times are always present: a zero time is the signal, and
	// encoding/json would not omit a struct field anyway.
	LeaseUntil time.Time `json:"lease_until"`
	Priority   int       `json:"priority"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	ClosedAt   time.Time `json:"closed_at"`
}

// Terminal reports whether a status ends a task's life.
func (s TaskStatus) Terminal() bool {
	return s == TaskDone || s == TaskCancelled
}

// Claimable reports whether a member may take the task now.
func (s TaskStatus) Claimable() bool {
	return s == TaskOpen || s == TaskOrphaned || s == TaskBlocked
}
