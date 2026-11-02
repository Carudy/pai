package session

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Carudy/pai/internal/core"
)

// Board errors. They are distinguishable so the tool layer can return a
// self-correcting observation (re-read with the current version) instead of a
// generic failure.
var (
	ErrTaskStale        = errors.New("task was changed by someone else")
	ErrTaskNotOwner     = errors.New("task is owned by another session")
	ErrTaskNotClaimable = errors.New("task cannot be claimed")
)

// TaskStore is the group board. Tasks belong to a group and reference their
// owner by immutable session id; deleting a group removes its board, deleting a
// session does not (the task survives, ownerless, so the work is not lost).
//
// Writes are optimistic: UpdateTask carries the version the caller read, and a
// mismatch is rejected with the current version. A claim is a lease, so an
// abandoned task becomes claimable again without anyone deciding it is dead.
type TaskStore interface {
	CreateTask(t core.Task) (core.Task, error)
	// Task loads one task, or ErrNotFound.
	Task(id string) (core.Task, error)
	// Tasks lists a group's board, actionable first.
	Tasks(groupID string, statuses []core.TaskStatus, limit int) ([]core.Task, error)
	// ClaimTask takes or renews a task, setting a lease. It refuses work that a
	// live claim holds and terminal work.
	ClaimTask(id, ownerID string, now time.Time, lease time.Duration) (core.Task, error)
	// UpdateTask applies one transition. expectVersion must match; allowForeign
	// is the host's permission for cancelling another member's task (a lead).
	UpdateTask(id, actorID string, expectVersion int, status core.TaskStatus, note string, allowForeign bool, now time.Time) (core.Task, error)
	// ExpireTasks orphans claims whose lease has lapsed.
	ExpireTasks(now time.Time) ([]core.Task, error)
	// OrphanTasksOwnedBy releases work held by a session that stopped or left.
	OrphanTasksOwnedBy(sessionID string, now time.Time) ([]core.Task, error)
	// DueEscalations reports tasks the host should mention to the lead once.
	DueEscalations(groupID string, now time.Time) ([]core.Task, error)
	// MarkTaskEscalated records that an escalation was sent; false means another
	// activation already sent it.
	MarkTaskEscalated(id string, kind core.TaskStatus, now time.Time) (bool, error)
}

var _ TaskStore = (*sqliteStore)(nil)

const taskColumns = `t.id, t.group_id, t.title, t.detail, t.status, COALESCE(t.owner_id, ''),
COALESCE(s.name, ''), t.version, t.lease_until, t.priority, t.created_at, t.updated_at, t.closed_at`

func (s *sqliteStore) CreateTask(t core.Task) (core.Task, error) {
	t.Title = strings.TrimSpace(t.Title)
	if t.Title == "" {
		return core.Task{}, errors.New("a task needs a title")
	}
	if len(t.Title) > core.MaxTaskTitle {
		return core.Task{}, fmt.Errorf("title is longer than %d bytes", core.MaxTaskTitle)
	}
	if len(t.Detail) > core.MaxTaskDetail {
		return core.Task{}, fmt.Errorf("detail is longer than %d bytes", core.MaxTaskDetail)
	}
	if t.Priority == 0 {
		t.Priority = 50
	}
	if t.Priority < 1 || t.Priority > 100 {
		return core.Task{}, errors.New("priority must be between 1 and 100")
	}
	now := time.Now().Unix()
	if t.ID == "" {
		t.ID = newID()
	}
	if _, err := s.db.Exec(`INSERT INTO tasks
(id, group_id, title, detail, status, version, priority, created_at, updated_at)
VALUES (?, ?, ?, ?, 'open', 1, ?, ?, ?)`,
		t.ID, t.GroupID, t.Title, t.Detail, t.Priority, now, now); err != nil {
		return core.Task{}, fmt.Errorf("create task: %w", err)
	}
	return s.Task(t.ID)
}

func (s *sqliteStore) Task(id string) (core.Task, error) {
	row := s.db.QueryRow(`SELECT `+taskColumns+`
FROM tasks t LEFT JOIN sessions s ON s.id = t.owner_id WHERE t.id = ?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Task{}, ErrNotFound
	}
	if err != nil {
		return core.Task{}, err
	}
	return t, nil
}

// Tasks lists a group's board, actionable first: open and orphaned work needs a
// member, blocked work needs someone to unblock it, and claimed work is in
// flight. Terminal rows are last and usually filtered out by the caller.
func (s *sqliteStore) Tasks(groupID string, statuses []core.TaskStatus, limit int) ([]core.Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + taskColumns + `
FROM tasks t LEFT JOIN sessions s ON s.id = t.owner_id
WHERE t.group_id = ?`
	args := []any{groupID}
	if len(statuses) > 0 {
		query += ` AND t.status IN (` + placeholders(len(statuses)) + `)`
		for _, st := range statuses {
			args = append(args, string(st))
		}
	}
	query += ` ORDER BY CASE t.status
  WHEN 'open' THEN 0 WHEN 'orphaned' THEN 1 WHEN 'blocked' THEN 2
  WHEN 'claimed' THEN 3 ELSE 4 END, t.priority DESC, t.created_at
LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *sqliteStore) ClaimTask(id, ownerID string, now time.Time, lease time.Duration) (core.Task, error) {
	if lease <= 0 {
		lease = core.DefaultTaskLease
	}
	res, err := s.db.Exec(`UPDATE tasks
SET status = 'claimed', owner_id = ?, lease_until = ?, version = version + 1, updated_at = ?
WHERE id = ? AND (
  status IN ('open', 'orphaned')
  OR owner_id = ?
  OR (status = 'claimed' AND lease_until IS NOT NULL AND lease_until < ?)
)`, ownerID, now.Add(lease).Unix(), now.Unix(), id, ownerID, now.Unix())
	if err != nil {
		return core.Task{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		current, err := s.Task(id)
		switch {
		case errors.Is(err, ErrNotFound):
			return core.Task{}, ErrNotFound
		case err != nil:
			return core.Task{}, err
		case current.Status.Terminal():
			return core.Task{}, fmt.Errorf("%w: task is %s", ErrTaskNotClaimable, current.Status)
		case current.OwnerID != "" && current.OwnerID != ownerID:
			return core.Task{}, fmt.Errorf("%w: claimed by %s", ErrTaskNotOwner, ownerName(current))
		default:
			return core.Task{}, ErrTaskNotClaimable
		}
	}
	return s.Task(id)
}

// UpdateTask applies one transition and appends an optional note. The permission
// check is deliberately narrow: only the owner moves a task it holds, and only
// the host (allowForeign, used for a lead cancelling someone's work) may cancel
// another member's task.
func (s *sqliteStore) UpdateTask(id, actorID string, expectVersion int, status core.TaskStatus, note string, allowForeign bool, now time.Time) (core.Task, error) {
	current, err := s.Task(id)
	if err != nil {
		return core.Task{}, err
	}
	if current.Version != expectVersion {
		return core.Task{}, fmt.Errorf("%w: current version is %d", ErrTaskStale, current.Version)
	}
	if current.Status.Terminal() && status != current.Status {
		return core.Task{}, fmt.Errorf("%w: task is already %s", ErrTaskNotClaimable, current.Status)
	}
	owner := current.OwnerID
	switch status {
	case core.TaskDone, core.TaskClaimed, core.TaskBlocked, core.TaskOpen:
		if owner != actorID {
			return core.Task{}, fmt.Errorf("%w: owned by %s", ErrTaskNotOwner, ownerName(current))
		}
	case core.TaskCancelled:
		if owner != "" && owner != actorID && !allowForeign {
			return core.Task{}, fmt.Errorf("%w: owned by %s", ErrTaskNotOwner, ownerName(current))
		}
	case core.TaskOrphaned:
		if !allowForeign {
			return core.Task{}, errors.New("only the host can orphan a task")
		}
	default:
		return core.Task{}, fmt.Errorf("unknown task status %q", status)
	}

	detail := current.Detail
	if note = strings.TrimSpace(note); note != "" {
		if len(note) > core.MaxTaskNote {
			return core.Task{}, fmt.Errorf("note is longer than %d bytes", core.MaxTaskNote)
		}
		if detail != "" {
			detail += "\n---\n"
		}
		detail += note
		if len(detail) > core.MaxTaskDetail {
			return core.Task{}, fmt.Errorf("the task detail would exceed %d bytes; close it and open a new one", core.MaxTaskDetail)
		}
	}
	ownerID := any(nil)
	var lease any
	switch status {
	case core.TaskClaimed:
		ownerID, lease = owner, now.Add(core.DefaultTaskLease).Unix()
	case core.TaskBlocked:
		ownerID = owner
	case core.TaskDone:
		ownerID = owner
	}
	var closed any
	if status.Terminal() {
		closed = now.Unix()
	}
	res, err := s.db.Exec(`UPDATE tasks
SET status = ?, owner_id = ?, detail = ?, lease_until = ?, closed_at = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`,
		string(status), ownerID, detail, lease, closed, now.Unix(), id, expectVersion)
	if err != nil {
		return core.Task{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.Task{}, ErrTaskStale
	}
	return s.Task(id)
}

func (s *sqliteStore) ExpireTasks(now time.Time) ([]core.Task, error) {
	return s.orphan(`status = 'claimed' AND lease_until IS NOT NULL AND lease_until < ?`, now.Unix(), now)
}

func (s *sqliteStore) OrphanTasksOwnedBy(sessionID string, now time.Time) ([]core.Task, error) {
	return s.orphan(`owner_id = ? AND status IN ('claimed', 'blocked')`, sessionID, now)
}

// orphan moves matching tasks to orphaned and returns them, in one transaction
// so a crashed sweep cannot leave half a board updated.
func (s *sqliteStore) orphan(where string, arg any, now time.Time) ([]core.Task, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM tasks WHERE `+where, arg)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(ids) == 0 {
		return nil, tx.Commit()
	}
	if _, err := tx.Exec(`UPDATE tasks
SET status = 'orphaned', owner_id = NULL, lease_until = NULL, version = version + 1, updated_at = ?
WHERE id IN (`+placeholders(len(ids))+`)`, append([]any{now.Unix()}, stringValues(ids)...)...); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := make([]core.Task, 0, len(ids))
	for _, id := range ids {
		t, err := s.Task(id)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (s *sqliteStore) DueEscalations(groupID string, now time.Time) ([]core.Task, error) {
	rows, err := s.db.Query(`SELECT `+taskColumns+`
FROM tasks t LEFT JOIN sessions s ON s.id = t.owner_id
WHERE t.group_id = ? AND (
  (t.status = 'blocked' AND t.updated_at <= ? AND t.escalated_blocked_at IS NULL)
  OR (t.status = 'orphaned' AND t.escalated_orphaned_at IS NULL)
)`, groupID, now.Add(-core.BlockedEscalationAfter).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *sqliteStore) MarkTaskEscalated(id string, kind core.TaskStatus, now time.Time) (bool, error) {
	column := ""
	switch kind {
	case core.TaskBlocked:
		column = "escalated_blocked_at"
	case core.TaskOrphaned:
		column = "escalated_orphaned_at"
	default:
		return false, fmt.Errorf("no escalation for task status %q", kind)
	}
	res, err := s.db.Exec(`UPDATE tasks SET `+column+` = ? WHERE id = ? AND `+column+` IS NULL`, now.Unix(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// scanTask reads one row of taskColumns.
func scanTask(row interface{ Scan(...any) error }) (core.Task, error) {
	var (
		t       core.Task
		status  string
		lease   sql.NullInt64
		created int64
		updated int64
		closed  sql.NullInt64
	)
	if err := row.Scan(&t.ID, &t.GroupID, &t.Title, &t.Detail, &status, &t.OwnerID, &t.OwnerName,
		&t.Version, &lease, &t.Priority, &created, &updated, &closed); err != nil {
		return core.Task{}, err
	}
	t.Status = core.TaskStatus(status)
	t.LeaseUntil = nullTime(lease)
	t.CreatedAt = time.Unix(created, 0)
	t.UpdatedAt = time.Unix(updated, 0)
	t.ClosedAt = nullTime(closed)
	return t, nil
}

func ownerName(t core.Task) string {
	if t.OwnerName != "" {
		return t.OwnerName
	}
	return "another session"
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func stringValues(ids []string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}
