package session

import (
	"fmt"
	"time"

	"github.com/Carudy/pai/internal/core"
)

// ActivityStore reads the material for a group's timeline: the durable events
// addressed to its members and the reports they made. It is optional, like the
// other group ports, so a backend without it simply shows a group without a
// timeline.
//
// The two sides are separate on purpose. Events are keyed by the immutable
// session id and are what the host delivered (mail, a user instruction, an
// escalation); turns are keyed by display name and are what a member said back
// (an `inform` or `done` report). Merging them is presentation, so it does not
// happen here.
type ActivityStore interface {
	// GroupEvents returns the most recent events addressed to any of these
	// sessions, newest first. State is filled in, so a reader can tell delivered
	// work from a retry or a dead letter.
	GroupEvents(sessionIDs []string, kinds []string, limit int) ([]core.Event, error)
	// RecentOutputs returns the most recent assistant outputs for these session
	// names, newest first. The store does not interpret the content; the caller
	// parses the action out of it.
	RecentOutputs(names []string, limit int) ([]NamedTurn, error)
}

// NamedTurn is one turn together with the session it belongs to. A cross-session
// read has no other way to say whose turn it is, and `core.Turn` deliberately
// does not carry a name.
type NamedTurn struct {
	Name string
	Turn core.Turn
}

var _ ActivityStore = (*sqliteStore)(nil)

// groupEventKinds is the host-side subset a timeline shows: the messages a group
// actually exchanges. Probes and timers are per-member internals and would only
// bury the conversation.
var groupEventKinds = []string{"mail", "broadcast", "user_input", "escalation"}

func (s *sqliteStore) GroupEvents(sessionIDs []string, kinds []string, limit int) ([]core.Event, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}
	if len(kinds) == 0 {
		kinds = groupEventKinds
	}
	if limit <= 0 || limit > 500 {
		limit = 40
	}
	args := make([]any, 0, len(sessionIDs)+len(kinds)+1)
	args = append(args, stringValues(sessionIDs)...)
	for _, kind := range kinds {
		args = append(args, kind)
	}
	args = append(args, limit)
	query := `SELECT id, session_id, class, priority, kind, source, payload, COALESCE(dedup_key, ''),
       created_at, deadline, COALESCE(trace, ''), hops, attempts, state
FROM events
WHERE session_id IN (` + placeholders(len(sessionIDs)) + `)
  AND kind IN (` + placeholders(len(kinds)) + `)
ORDER BY created_at DESC, id DESC
LIMIT ?`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("read group events: %w", err)
	}
	return scanEvents(rows)
}

func (s *sqliteStore) RecentOutputs(names []string, limit int) ([]NamedTurn, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 40
	}
	args := make([]any, 0, len(names)+3)
	args = append(args, stringValues(names)...)
	args = append(args, "assistant", core.KindOutput, limit)
	rows, err := s.db.Query(`SELECT name, role, kind, content, at, seq
FROM turns
WHERE name IN (`+placeholders(len(names))+`) AND role = ? AND kind = ?
ORDER BY at DESC, seq DESC
LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("read group outputs: %w", err)
	}
	defer rows.Close()
	out := make([]NamedTurn, 0, limit)
	for rows.Next() {
		var (
			nt NamedTurn
			at int64
		)
		if err := rows.Scan(&nt.Name, &nt.Turn.Role, &nt.Turn.Kind, &nt.Turn.Content, &at, &nt.Turn.Seq); err != nil {
			return nil, err
		}
		nt.Turn.At = time.Unix(at, 0)
		out = append(out, nt)
	}
	return out, rows.Err()
}
