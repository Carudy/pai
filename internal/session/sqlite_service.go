package session

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Carudy/pai/internal/core"
)

// ServiceInfo returns one service's metadata, status, and manifest spec.
func (s *sqliteStore) ServiceInfo(name string) (*ServiceInfo, error) {
	var (
		info      ServiceInfo
		createdAt int64
		updatedAt int64
		nextWake  sql.NullInt64
	)
	err := s.db.QueryRow(`
SELECT name, id, role, model, cwd, title, created_at, updated_at,
       prompt_tokens, prompt_estimate, mode, state, next_wake, status, spec
FROM sessions WHERE name = ?`, name).Scan(
		&info.Name, &info.ID, &info.Role, &info.Model, &info.Cwd, &info.Title,
		&createdAt, &updatedAt, &info.PromptTokens, &info.PromptEstimate,
		&info.Mode, &info.State, &nextWake, &info.Status, &info.Spec)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	info.CreatedAt, info.UpdatedAt = time.Unix(createdAt, 0), time.Unix(updatedAt, 0)
	info.NextWake = unixPtr(nextWake)
	return &info, nil
}

func (s *sqliteStore) SetServiceState(name, state, status string) error {
	res, err := s.db.Exec(`UPDATE sessions SET state = ?, status = ? WHERE name = ?`, state, status, name)
	return changed(res, err, name)
}

func (s *sqliteStore) SetNextWake(name string, at *time.Time) error {
	res, err := s.db.Exec(`UPDATE sessions SET next_wake = ? WHERE name = ?`, timeArg(at), name)
	return changed(res, err, name)
}

func (s *sqliteStore) SetServiceSpec(name, spec string) error {
	res, err := s.db.Exec(`UPDATE sessions SET spec = ?, mode = 'service' WHERE name = ?`, spec, name)
	return changed(res, err, name)
}

// changed turns an UPDATE into an ErrNotFound when no row matched.
func changed(res sql.Result, err error, name string) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return nil
}

// AppendEvent enqueues an event, replacing an existing pending event for the
// same dedupe key. A session id, not a display name, is required: a rename must
// not orphan queued work.
func (s *sqliteStore) AppendEvent(ev core.Event) (int64, error) {
	if strings.TrimSpace(ev.Session) == "" {
		return 0, errors.New("event session id is empty")
	}
	at := ev.At
	if at.IsZero() {
		at = time.Now()
	}
	var deadline any
	if !ev.Deadline.IsZero() {
		deadline = ev.Deadline.Unix()
	}
	priority := ev.Priority
	if priority == 0 {
		priority = ev.Class.DefaultPriority()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if ev.DedupKey != "" {
		var id int64
		err := tx.QueryRow(`SELECT id FROM events WHERE session_id = ? AND dedup_key = ? AND state = 'pending'`,
			ev.Session, ev.DedupKey).Scan(&id)
		switch {
		case err == nil:
			if _, err := tx.Exec(`UPDATE events
SET class = ?, priority = ?, kind = ?, source = ?, payload = ?, created_at = ?, available_at = ?,
    deadline = ?, attempts = 0, last_error = NULL
WHERE id = ?`,
				int(ev.Class), priority, ev.Kind, ev.Source, ev.Payload, at.Unix(), at.Unix(), deadline, id); err != nil {
				return 0, err
			}
			return id, tx.Commit()
		case !errors.Is(err, sql.ErrNoRows):
			return 0, err
		}
	}

	res, err := tx.Exec(`INSERT INTO events
(session_id, class, priority, kind, source, payload, dedup_key, created_at, available_at, deadline, attempts, state)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending')`,
		ev.Session, int(ev.Class), priority, ev.Kind, ev.Source, ev.Payload, nullString(ev.DedupKey),
		at.Unix(), at.Unix(), deadline, ev.Attempts)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *sqliteStore) DueEvents(sessionID string, now time.Time, limit int) ([]core.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.Query(`
SELECT id, session_id, class, priority, kind, source, payload, COALESCE(dedup_key, ''),
       created_at, deadline, attempts
FROM events
WHERE session_id = ? AND state = 'pending' AND available_at <= ?
ORDER BY priority DESC, (deadline IS NULL), deadline, created_at, id
LIMIT ?`, sessionID, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// QueuedEvents lists what is waiting, including events whose retry time has not
// arrived and events leased by a worker. The scheduler only ever acts on
// DueEvents; this is for inspection.
func (s *sqliteStore) QueuedEvents(sessionID string, limit int) ([]core.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.Query(`
SELECT id, session_id, class, priority, kind, source, payload, COALESCE(dedup_key, ''),
       created_at, deadline, attempts
FROM events
WHERE session_id = ? AND state IN ('pending', 'leased')
ORDER BY priority DESC, (deadline IS NULL), deadline, created_at, id
LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

func scanEvents(rows *sql.Rows) ([]core.Event, error) {
	defer rows.Close()
	var events []core.Event
	for rows.Next() {
		var (
			ev       core.Event
			class    int
			created  int64
			deadline sql.NullInt64
		)
		if err := rows.Scan(&ev.ID, &ev.Session, &class, &ev.Priority, &ev.Kind, &ev.Source, &ev.Payload,
			&ev.DedupKey, &created, &deadline, &ev.Attempts); err != nil {
			return nil, err
		}
		ev.Class = core.EventClass(class)
		ev.At = time.Unix(created, 0)
		ev.Deadline = nullTime(deadline)
		events = append(events, ev)
	}
	return events, rows.Err()
}

func (s *sqliteStore) LeaseEvents(ids []int64, until time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	query := `UPDATE events SET state = 'leased', lease_until = ? WHERE state = 'pending' AND id IN (` + idPlaceholders(len(ids)) + `)`
	args := append([]any{until.Unix()}, idValues(ids)...)
	_, err := s.db.Exec(query, args...)
	return err
}

func (s *sqliteStore) AckEvents(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	query := `UPDATE events SET state = 'done', lease_until = NULL WHERE id IN (` + idPlaceholders(len(ids)) + `)`
	_, err := s.db.Exec(query, idValues(ids)...)
	return err
}

// NackEvent returns an event to pending, or moves it to dead when the caller has
// exhausted its attempts. attempts is incremented either way.
func (s *sqliteStore) NackEvent(id int64, retryAt time.Time, lastErr string, dead bool) error {
	state := "pending"
	if dead {
		state = "dead"
	}
	_, err := s.db.Exec(`UPDATE events
SET state = ?, attempts = attempts + 1, available_at = ?, lease_until = NULL, last_error = ?
WHERE id = ?`, state, retryAt.Unix(), lastErr, id)
	return err
}

func (s *sqliteStore) PendingEvents(sessionID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM events WHERE session_id = ? AND state IN ('pending', 'leased')`, sessionID).Scan(&n)
	return n, err
}

func (s *sqliteStore) ResetLeases() error {
	_, err := s.db.Exec(`UPDATE events SET state = 'pending', lease_until = NULL WHERE state = 'leased'`)
	return err
}

func (s *sqliteStore) PutWatcher(w Watcher) error {
	if strings.TrimSpace(w.ID) == "" {
		w.ID = newID()
	}
	version := w.Version
	if version <= 0 {
		version = 1
	}
	// approved_at is part of the replacement, not preserved from the old row:
	// the caller computed it (kept for an unchanged probe, cleared for a changed
	// command), and an upsert that silently kept an old grant would let an edit
	// bless code the user never approved.
	_, err := s.db.Exec(`
INSERT INTO watchers(id, session_id, name, spec, enabled, approved_at, last_run, next_run, last_state, last_error, version)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id, name) DO UPDATE SET
  spec = excluded.spec,
  enabled = excluded.enabled,
  approved_at = excluded.approved_at,
  next_run = excluded.next_run,
  last_state = excluded.last_state,
  last_error = excluded.last_error,
  version = watchers.version + 1`,
		w.ID, w.SessionID, w.Name, w.Spec, boolInt(w.Enabled), timeArg(w.ApprovedAt),
		timeArg(w.LastRun), timeArg(w.NextRun), w.LastState, w.LastError, version)
	return err
}

func (s *sqliteStore) Watchers(sessionID string) ([]Watcher, error) {
	rows, err := s.db.Query(`
SELECT id, session_id, name, spec, enabled, approved_at, last_run, next_run, last_state, last_error, version
FROM watchers WHERE session_id = ? ORDER BY name`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var watchers []Watcher
	for rows.Next() {
		var (
			w        Watcher
			enabled  int
			approved sql.NullInt64
			lastRun  sql.NullInt64
			nextRun  sql.NullInt64
		)
		if err := rows.Scan(&w.ID, &w.SessionID, &w.Name, &w.Spec, &enabled, &approved,
			&lastRun, &nextRun, &w.LastState, &w.LastError, &w.Version); err != nil {
			return nil, err
		}
		w.Enabled = enabled != 0
		w.ApprovedAt, w.LastRun, w.NextRun = unixPtr(approved), unixPtr(lastRun), unixPtr(nextRun)
		watchers = append(watchers, w)
	}
	return watchers, rows.Err()
}

func (s *sqliteStore) SetWatcherState(id string, lastRun, nextRun *time.Time, lastState, lastErr string) error {
	_, err := s.db.Exec(`UPDATE watchers SET last_run = ?, next_run = ?, last_state = ?, last_error = ? WHERE id = ?`,
		timeArg(lastRun), timeArg(nextRun), lastState, lastErr, id)
	return err
}

func (s *sqliteStore) DeleteWatcher(id string) error {
	_, err := s.db.Exec(`DELETE FROM watchers WHERE id = ?`, id)
	return err
}

func idPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func idValues(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func unixPtr(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.Unix(n.Int64, 0)
	return &t
}

func nullTime(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return time.Unix(n.Int64, 0)
}

func timeArg(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.Unix()
}
