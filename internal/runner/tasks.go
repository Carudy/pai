package runner

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Carudy/pai/internal/core"
)

// boardSweepInterval keeps the lease-expiry and escalation pass off the hot
// path: it runs on the first activation of a group in each interval, not on
// every wake of every member.
const boardSweepInterval = time.Minute

// sessionTasks binds the board tools to the calling session. The model names a
// task; the session id, its group, and its right to touch the board are resolved
// here, so a payload cannot address another group's work.
type sessionTasks struct {
	m    *Manager
	from string
}

var _ core.Tasks = sessionTasks{}

// memberGroup resolves the caller's group, or explains why it has none.
func (t sessionTasks) memberGroup() (core.Group, string, error) {
	t.m.mu.Lock()
	id := t.m.sessionIDs[t.from]
	t.m.mu.Unlock()
	if id == "" {
		return core.Group{}, "", errors.New("this session is not a resident service")
	}
	groups, ok := t.m.backend.(GroupBackend)
	if !ok {
		return core.Group{}, "", errors.New("this host has no group directory")
	}
	g, in, err := groups.GroupBySession(id)
	if err != nil {
		return core.Group{}, "", fmt.Errorf("look up your group: %w", err)
	}
	if !in {
		return core.Group{}, "", errors.New("this session does not belong to a group")
	}
	return g, id, nil
}

func (t sessionTasks) store() (TaskBackend, error) {
	ts, ok := t.m.backend.(TaskBackend)
	if !ok {
		return nil, errors.New("this host has no group board")
	}
	return ts, nil
}

func (t sessionTasks) Add(title, detail string, priority int) (core.Task, error) {
	g, _, err := t.memberGroup()
	if err != nil {
		return core.Task{}, err
	}
	ts, err := t.store()
	if err != nil {
		return core.Task{}, err
	}
	return ts.CreateTask(core.Task{GroupID: g.ID, Title: title, Detail: detail, Priority: priority})
}

func (t sessionTasks) Claim(id string) (core.Task, error) {
	g, id2, err := t.memberGroup()
	if err != nil {
		return core.Task{}, err
	}
	ts, err := t.store()
	if err != nil {
		return core.Task{}, err
	}
	task, err := t.inGroup(ts, g, id)
	if err != nil {
		return core.Task{}, err
	}
	return ts.ClaimTask(task.ID, id2, time.Now(), core.DefaultTaskLease)
}

func (t sessionTasks) Update(id string, expectVersion int, status core.TaskStatus, note string) (core.Task, error) {
	g, id2, err := t.memberGroup()
	if err != nil {
		return core.Task{}, err
	}
	ts, err := t.store()
	if err != nil {
		return core.Task{}, err
	}
	task, err := t.inGroup(ts, g, id)
	if err != nil {
		return core.Task{}, err
	}
	// Only the lead may cancel another member's task; everything else requires
	// ownership, which the store enforces.
	lead := g.LeadID != "" && g.LeadID == id2
	return ts.UpdateTask(task.ID, id2, expectVersion, status, note, lead, time.Now())
}

func (t sessionTasks) List(status core.TaskStatus, limit int) ([]core.Task, error) {
	g, _, err := t.memberGroup()
	if err != nil {
		return nil, err
	}
	ts, err := t.store()
	if err != nil {
		return nil, err
	}
	statuses := []core.TaskStatus{status}
	if status == "" {
		// Default view: work that still matters. Terminal rows are readable on
		// request but would crowd the list a model reads every activation.
		statuses = []core.TaskStatus{core.TaskOpen, core.TaskOrphaned, core.TaskBlocked, core.TaskClaimed}
	}
	return ts.Tasks(g.ID, statuses, limit)
}

// inGroup refuses a task id that belongs to another group, so a typo cannot
// cross the membership boundary.
func (t sessionTasks) inGroup(ts TaskBackend, g core.Group, id string) (core.Task, error) {
	task, err := ts.Task(strings.TrimSpace(id))
	if err != nil {
		return core.Task{}, fmt.Errorf("task %q is not on your board", id)
	}
	if task.GroupID != g.ID {
		return core.Task{}, fmt.Errorf("task %q is not on your board", id)
	}
	return task, nil
}

// checkBoard runs the group's housekeeping at most once per interval: expire
// leases, orphan work held by a session that is gone, and tell the lead once
// about a task that is blocked or orphaned. It is best-effort — a board read
// failure is a diagnostic, never a reason to fail an activation.
func (m *Manager) checkBoard(w *worker, now time.Time) {
	ts, ok := m.backend.(TaskBackend)
	if !ok {
		return
	}
	groups, ok := m.backend.(GroupBackend)
	if !ok {
		return
	}
	m.mu.Lock()
	id, name := w.sessionID, w.name
	m.mu.Unlock()
	if id == "" {
		return
	}
	g, in, err := groups.GroupBySession(id)
	if err != nil || !in {
		return
	}
	if !m.boardDue(g.ID, now) {
		return
	}
	if orphaned, err := ts.ExpireTasks(now); err == nil {
		for _, t := range orphaned {
			m.diag(Diagnostic{Session: name, Kind: "board", Subject: t.ID, Outcome: "orphaned",
				Detail: fmt.Sprintf("lease expired; task %q is claimable again", t.Title)})
		}
	}
	due, err := ts.DueEscalations(g.ID, now)
	if err != nil {
		m.diag(Diagnostic{Session: name, Kind: "board", Outcome: "failed", Detail: err.Error()})
		return
	}
	for _, t := range due {
		if g.LeadName == "" {
			// Nothing to escalate to. Mark it so the sweep does not retry forever,
			// and leave the fact visible in the log.
			if _, err := ts.MarkTaskEscalated(t.ID, t.Status, now); err == nil {
				m.diag(Diagnostic{Session: name, Kind: "board", Subject: t.ID, Outcome: "skipped",
					Detail: "group has no lead; escalation not sent"})
			}
			continue
		}
		sent, err := ts.MarkTaskEscalated(t.ID, t.Status, now)
		if err != nil || !sent {
			continue
		}
		m.mu.Lock()
		leadID := m.sessionIDs[g.LeadName]
		m.mu.Unlock()
		if leadID == "" {
			continue
		}
		ev := core.Event{
			Session: leadID, Class: core.EventPeer, Kind: "escalation",
			Source: "group:" + g.Name, Payload: escalationText(t), At: now,
			Priority: core.PriorityPeer, Trace: core.NewTrace(),
		}
		if err := m.queueMail(g.LeadName, ev); err != nil {
			m.diag(Diagnostic{Session: name, Kind: "board", Subject: t.ID, Outcome: "failed",
				Detail: "escalation not delivered: " + err.Error()})
			continue
		}
		m.diag(Diagnostic{Session: name, Kind: "board", Subject: t.ID, Outcome: "escalated",
			Detail: "to " + g.LeadName})
	}
}

// boardDue throttles the sweep per group.
func (m *Manager) boardDue(groupID string, now time.Time) bool {
	m.boardMu.Lock()
	defer m.boardMu.Unlock()
	if m.boardChecks == nil {
		m.boardChecks = make(map[string]time.Time)
	}
	if last, ok := m.boardChecks[groupID]; ok && now.Sub(last) < boardSweepInterval {
		return false
	}
	m.boardChecks[groupID] = now
	return true
}

func escalationText(t core.Task) string {
	owner := t.OwnerName
	if owner == "" {
		owner = "unowned"
	}
	if t.Status == core.TaskOrphaned {
		return fmt.Sprintf("Task %s (%q) is orphaned: its lease expired or its owner left. It is claimable again.", t.ID, t.Title)
	}
	return fmt.Sprintf("Task %s (%q) has been blocked for %s (owner: %s). Check whether it needs help.",
		t.ID, t.Title, core.BlockedEscalationAfter, owner)
}
