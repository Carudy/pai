package session

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Carudy/pai/internal/core"
)

// GroupStore is the durable group directory. It is separate from Store so a
// backend that predates groups is still usable, and callers type-assert it the
// way they do ServiceStore.
//
// Membership references the immutable session id, never the display name: a
// rename must not move a session between groups, and deleting a session removes
// its membership through the foreign key.
type GroupStore interface {
	// CreateGroup stores a group and its initial members. The lead must be one of
	// them, and the member count may not exceed MaxMembers.
	CreateGroup(g core.Group, memberIDs []string) error
	// GroupByName returns a group by display name; false when there is none.
	GroupByName(name string) (core.Group, bool, error)
	// GroupBySession returns the group a session belongs to, if any. A session is
	// in at most one group in v1, so this is a single lookup, not a scan.
	GroupBySession(sessionID string) (core.Group, bool, error)
	// Groups lists every group, ordered by name.
	Groups() ([]core.Group, error)
	// GroupMembers lists a group's members, ordered by session name.
	GroupMembers(groupID string) ([]core.GroupMember, error)
	// AddGroupMember adds one existing session, or ErrNotFound. It enforces the
	// member cap and refuses a session that already belongs to another group.
	AddGroupMember(groupID, sessionID string) error
	// RemoveGroupMember removes a membership. The lead cannot be removed: a group
	// without a lead cannot be addressed, so that is a delete-and-recreate.
	RemoveGroupMember(groupID, sessionID string) error
	// SetGroupMemberIntro stores the operator's one-line description of what a
	// member does in this group; an empty string clears it. It is per membership,
	// not per session: the same session's in-group job is about this team.
	SetGroupMemberIntro(groupID, sessionID, intro string) error
	// DeleteGroup removes the directory rows. Member sessions and their
	// transcripts are untouched.
	DeleteGroup(name string) error
}

var _ GroupStore = (*sqliteStore)(nil)

// GroupFromNames resolves a group definition given by session names, which is how
// an operator writes one, into the durable form and the member ids the store
// needs. It is the single definition of "a member is a resident service": the
// CLI manifest and the web form both go through it, so they cannot disagree about
// which sessions may join. The lead, when set, is resolved the same way and its
// membership is left to CreateGroup to check.
func GroupFromNames(store Store, name, goal, lead string, members []string, maxMembers int) (core.Group, []string, error) {
	metas, err := store.List()
	if err != nil {
		return core.Group{}, nil, fmt.Errorf("list sessions: %w", err)
	}
	byName := make(map[string]Meta, len(metas))
	for _, m := range metas {
		byName[m.Name] = m
	}
	ids := make([]string, 0, len(members))
	for _, member := range members {
		meta, exists := byName[member]
		if !exists {
			return core.Group{}, nil, fmt.Errorf("%q is not a session; create it first", member)
		}
		if meta.Mode != ModeService {
			return core.Group{}, nil, fmt.Errorf("%q is a chat session; a group member must be a service session", member)
		}
		ids = append(ids, meta.ID)
	}
	leadID := ""
	if lead != "" {
		meta, exists := byName[lead]
		if !exists {
			return core.Group{}, nil, fmt.Errorf("the lead %q is not a session", lead)
		}
		leadID = meta.ID
	}
	return core.Group{Name: name, Goal: goal, LeadID: leadID, MaxMembers: maxMembers}, ids, nil
}

// ApplyGroupIntros stores the in-group descriptions a definition carries. It runs
// after CreateGroup because the descriptions hang off the membership rows; it
// looks the group up by name, since the id is the store's to assign. A caller
// that fails here should delete the group: a half described roster is worse than
// none.
func ApplyGroupIntros(gs GroupStore, groupName string, intros map[string]string) error {
	if len(intros) == 0 {
		return nil
	}
	g, found, err := gs.GroupByName(groupName)
	if err != nil {
		return fmt.Errorf("look up group: %w", err)
	}
	if !found {
		return ErrNotFound
	}
	members, err := gs.GroupMembers(g.ID)
	if err != nil {
		return fmt.Errorf("read members: %w", err)
	}
	byName := make(map[string]string, len(members))
	for _, m := range members {
		byName[m.Name] = m.SessionID
	}
	// Sorted so a rejected description is always the same one.
	names := make([]string, 0, len(intros))
	for name := range intros {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		id, ok := byName[name]
		if !ok {
			return fmt.Errorf("%q is not a member of the group", name)
		}
		if err := gs.SetGroupMemberIntro(g.ID, id, intros[name]); err != nil {
			return fmt.Errorf("describe %s: %w", name, err)
		}
	}
	return nil
}

// CreateGroup writes the group and its members in one transaction, so a partial
// membership can never be visible to a mail delivery.
func (s *sqliteStore) CreateGroup(g core.Group, memberIDs []string) error {
	if err := validateGroupName(g.Name); err != nil {
		return err
	}
	if len(memberIDs) == 0 {
		return errors.New("a group needs at least one member")
	}
	if g.MaxMembers <= 0 {
		g.MaxMembers = 8
	}
	if len(memberIDs) > g.MaxMembers {
		return fmt.Errorf("group %q has %d members but max_members is %d", g.Name, len(memberIDs), g.MaxMembers)
	}
	if g.ID == "" {
		g.ID = newID()
	}
	if g.LeadID != "" {
		found := false
		for _, id := range memberIDs {
			if id == g.LeadID {
				found = true
				break
			}
		}
		if !found {
			return errors.New("the group lead must be one of its members")
		}
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO groups (id, name, goal, lead_id, max_members, created_at)
VALUES (?, ?, ?, ?, ?, ?)`, g.ID, g.Name, g.Goal, g.LeadID, g.MaxMembers, now); err != nil {
		return fmt.Errorf("create group %q: %w", g.Name, err)
	}
	for _, id := range memberIDs {
		var name string
		if err := tx.QueryRow(`SELECT name FROM sessions WHERE id = ?`, id).Scan(&name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("session %q does not exist", id)
			}
			return err
		}
		var grouped int
		if err := tx.QueryRow(`SELECT COUNT(1) FROM group_members WHERE session_id = ?`, id).Scan(&grouped); err != nil {
			return err
		}
		if grouped > 0 {
			return fmt.Errorf("%s already belongs to a group", name)
		}
		if _, err := tx.Exec(`INSERT INTO group_members (group_id, session_id, joined_at) VALUES (?, ?, ?)`,
			g.ID, id, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// validateGroupName mirrors the session-name rule: a group is addressed from a
// payload, so it must not be able to carry punctuation that a parser would
// interpret differently later.
func validateGroupName(name string) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid group name %q (allowed: letters, digits, . _ -)", name)
	}
	return nil
}

func (s *sqliteStore) GroupByName(name string) (core.Group, bool, error) {
	var g core.Group
	err := s.db.QueryRow(`SELECT g.id, g.name, g.goal, g.lead_id, COALESCE(l.name, ''), g.max_members,
  (SELECT COUNT(1) FROM group_members m WHERE m.group_id = g.id)
FROM groups g LEFT JOIN sessions l ON l.id = g.lead_id
WHERE g.name = ?`, name).Scan(&g.ID, &g.Name, &g.Goal, &g.LeadID, &g.LeadName, &g.MaxMembers, &g.Members)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Group{}, false, nil
	}
	if err != nil {
		return core.Group{}, false, err
	}
	return g, true, nil
}

func (s *sqliteStore) GroupBySession(sessionID string) (core.Group, bool, error) {
	var g core.Group
	err := s.db.QueryRow(`SELECT g.id, g.name, g.goal, g.lead_id, COALESCE(l.name, ''), g.max_members,
  (SELECT COUNT(1) FROM group_members m2 WHERE m2.group_id = g.id)
FROM groups g
JOIN group_members m ON m.group_id = g.id
LEFT JOIN sessions l ON l.id = g.lead_id
WHERE m.session_id = ?`, sessionID).Scan(&g.ID, &g.Name, &g.Goal, &g.LeadID, &g.LeadName, &g.MaxMembers, &g.Members)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Group{}, false, nil
	}
	if err != nil {
		return core.Group{}, false, err
	}
	return g, true, nil
}

func (s *sqliteStore) Groups() ([]core.Group, error) {
	rows, err := s.db.Query(`SELECT g.id, g.name, g.goal, g.lead_id, COALESCE(l.name, ''), g.max_members,
  (SELECT COUNT(1) FROM group_members m WHERE m.group_id = g.id)
FROM groups g LEFT JOIN sessions l ON l.id = g.lead_id ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Group
	for rows.Next() {
		var g core.Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Goal, &g.LeadID, &g.LeadName, &g.MaxMembers, &g.Members); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *sqliteStore) GroupMembers(groupID string) ([]core.GroupMember, error) {
	rows, err := s.db.Query(`SELECT m.group_id, m.session_id, s.name, COALESCE(m.intro, '')
FROM group_members m JOIN sessions s ON s.id = m.session_id
WHERE m.group_id = ? ORDER BY s.name`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.GroupMember
	for rows.Next() {
		var m core.GroupMember
		if err := rows.Scan(&m.GroupID, &m.SessionID, &m.Name, &m.Intro); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetGroupMemberIntro stores what a member does for this group. An empty string
// clears it; the row must already exist, so a typo in a member name is an error
// rather than a silent write to nothing.
func (s *sqliteStore) SetGroupMemberIntro(groupID, sessionID, intro string) error {
	res, err := s.db.Exec(`UPDATE group_members SET intro = ? WHERE group_id = ? AND session_id = ?`,
		intro, groupID, sessionID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *sqliteStore) AddGroupMember(groupID, sessionID string) error {
	var existing string
	err := s.db.QueryRow(`SELECT group_id FROM group_members WHERE session_id = ?`, sessionID).Scan(&existing)
	switch {
	case err == nil && existing == groupID:
		// Already a member: the operation is a no-op rather than a constraint error,
		// so repeating an `add` is harmless.
		return nil
	case err == nil:
		return errors.New("the session already belongs to another group")
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	var max int
	if err := s.db.QueryRow(`SELECT max_members FROM groups WHERE id = ?`, groupID).Scan(&max); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM group_members WHERE group_id = ?`, groupID).Scan(&count); err != nil {
		return err
	}
	if count >= max {
		return fmt.Errorf("the group is full (%d members)", max)
	}
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM sessions WHERE id = ?`, sessionID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	_, err = s.db.Exec(`INSERT INTO group_members (group_id, session_id, joined_at) VALUES (?, ?, ?)`,
		groupID, sessionID, time.Now().Unix())
	return err
}

func (s *sqliteStore) RemoveGroupMember(groupID, sessionID string) error {
	var lead string
	if err := s.db.QueryRow(`SELECT lead_id FROM groups WHERE id = ?`, groupID).Scan(&lead); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if lead == sessionID {
		return errors.New("the lead cannot be removed; delete the group or make another member the lead")
	}
	res, err := s.db.Exec(`DELETE FROM group_members WHERE group_id = ? AND session_id = ?`, groupID, sessionID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *sqliteStore) DeleteGroup(name string) error {
	res, err := s.db.Exec(`DELETE FROM groups WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
