package session

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/core"
)

// boardFixture creates a group with two members so ownership rules can be
// exercised against a real store.
func boardFixture(t *testing.T) (*sqliteStore, core.Group, string, string) {
	t.Helper()
	s := openSQLiteStore(t)
	lead := makeService(t, s, "coord")
	a := makeService(t, s, "hydro")
	b := makeService(t, s, "met")
	if err := s.CreateGroup(core.Group{Name: "g", LeadID: lead}, []string{lead, a, b}); err != nil {
		t.Fatal(err)
	}
	g, ok, err := s.GroupByName("g")
	if err != nil || !ok {
		t.Fatalf("GroupByName = %v %v", ok, err)
	}
	return s, g, a, b
}

// The board is optimistic: a claim is exclusive while its lease is live, and a
// write carrying an old version is rejected with the version to re-read.
func TestTaskBoardLifecycle(t *testing.T) {
	s, g, a, b := boardFixture(t)
	now := time.Now()

	task, err := s.CreateTask(core.Task{GroupID: g.ID, Title: "check the gate"})
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != core.TaskOpen || task.Version != 1 || task.Priority != 50 {
		t.Fatalf("task = %+v", task)
	}

	claimed, err := s.ClaimTask(task.ID, a, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != core.TaskClaimed || claimed.OwnerID != a || claimed.OwnerName != "hydro" || claimed.LeaseUntil.IsZero() {
		t.Fatalf("claim = %+v", claimed)
	}
	if _, err := s.ClaimTask(task.ID, b, now, time.Minute); !errors.Is(err, ErrTaskNotOwner) {
		t.Fatalf("stealing a live claim = %v", err)
	}

	// A stale write names the version to read.
	if _, err := s.UpdateTask(task.ID, a, task.Version, core.TaskDone, "", false, now); !errors.Is(err, ErrTaskStale) {
		t.Fatalf("stale update = %v", err)
	}
	done, err := s.UpdateTask(task.ID, a, claimed.Version, core.TaskDone, "gate open", false, now)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != core.TaskDone || done.ClosedAt.IsZero() || !strings.Contains(done.Detail, "gate open") {
		t.Fatalf("done = %+v", done)
	}
	if _, err := s.UpdateTask(task.ID, a, done.Version, core.TaskCancelled, "", false, now); err == nil {
		t.Fatal("a terminal task moved")
	}

	// A foreigner cannot cancel work another member holds; the lead can. Unowned
	// work is fair game, since no one is mid-task on it.
	other, err := s.CreateTask(core.Task{GroupID: g.ID, Title: "second"})
	if err != nil {
		t.Fatal(err)
	}
	held, err := s.ClaimTask(other.ID, a, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(other.ID, b, held.Version, core.TaskCancelled, "", false, now); !errors.Is(err, ErrTaskNotOwner) {
		t.Fatalf("foreign cancel = %v", err)
	}
	if _, err := s.UpdateTask(other.ID, g.LeadID, held.Version, core.TaskCancelled, "", true, now); err != nil {
		t.Fatalf("lead cancel = %v", err)
	}
}

// A lapsed lease returns the task to the group as orphaned, and the escalation
// marker makes the host notify the lead exactly once.
func TestTaskLeaseExpiryOrphansOnce(t *testing.T) {
	s, g, a, _ := boardFixture(t)
	now := time.Now()

	task, err := s.CreateTask(core.Task{GroupID: g.ID, Title: "watch the gauge"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimTask(task.ID, a, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if orphaned, err := s.ExpireTasks(now.Add(30 * time.Second)); err != nil || len(orphaned) != 0 {
		t.Fatalf("early expiry = %v %v", orphaned, err)
	}
	orphaned, err := s.ExpireTasks(now.Add(2 * time.Minute))
	if err != nil || len(orphaned) != 1 {
		t.Fatalf("expiry = %v %v", orphaned, err)
	}
	if got := orphaned[0]; got.Status != core.TaskOrphaned || got.OwnerID != "" || !got.LeaseUntil.IsZero() {
		t.Fatalf("orphaned task = %+v", got)
	}

	due, err := s.DueEscalations(g.ID, now.Add(2*time.Minute))
	if err != nil || len(due) != 1 || due[0].ID != task.ID {
		t.Fatalf("due escalations = %+v, %v", due, err)
	}
	first, err := s.MarkTaskEscalated(task.ID, core.TaskOrphaned, now)
	if err != nil || !first {
		t.Fatalf("first escalation = %v %v", first, err)
	}
	second, err := s.MarkTaskEscalated(task.ID, core.TaskOrphaned, now)
	if err != nil || second {
		t.Fatalf("second escalation = %v %v", second, err)
	}
	if due, _ := s.DueEscalations(g.ID, now.Add(3*time.Minute)); len(due) != 0 {
		t.Fatalf("escalation repeated: %+v", due)
	}
}

// A blocked task waits: it escalates only after the configured window, and its
// owner keeps it.
func TestBlockedTaskEscalatesAfterTheWindow(t *testing.T) {
	s, g, a, _ := boardFixture(t)
	now := time.Now()

	task, err := s.CreateTask(core.Task{GroupID: g.ID, Title: "coordinate the dive team"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimTask(task.ID, a, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := s.UpdateTask(task.ID, a, claimed.Version, core.TaskBlocked, "waiting for clearance", false, now)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.OwnerID != a {
		t.Fatalf("a blocked task lost its owner: %+v", blocked)
	}
	if due, _ := s.DueEscalations(g.ID, now.Add(core.BlockedEscalationAfter-time.Minute)); len(due) != 0 {
		t.Fatalf("escalated early: %+v", due)
	}
	if due, _ := s.DueEscalations(g.ID, now.Add(core.BlockedEscalationAfter+time.Minute)); len(due) != 1 {
		t.Fatalf("not escalated after the window: %+v", due)
	}
}

// Deleting a session releases its work; deleting the group removes the board
// with it.
func TestBoardSurvivesOwnerDeletionAndDiesWithTheGroup(t *testing.T) {
	s, g, a, _ := boardFixture(t)
	task, err := s.CreateTask(core.Task{GroupID: g.ID, Title: "handoff"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimTask(task.ID, a, time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	orphaned, err := s.OrphanTasksOwnedBy(a, time.Now())
	if err != nil || len(orphaned) != 1 || orphaned[0].Status != core.TaskOrphaned {
		t.Fatalf("orphan by owner = %+v %v", orphaned, err)
	}
	if err := s.DeleteGroup("g"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Task(task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("board survived its group: %v", err)
	}
}
