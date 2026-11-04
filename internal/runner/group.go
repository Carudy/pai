package runner

import (
	"errors"
	"fmt"
	"time"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/core"
)

// GroupMemberView is one roster row with the live-ish facts an operator wants:
// role, last persisted state, and next wake. None of it enters the prompt.
type GroupMemberView struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Role     string     `json:"role,omitempty"`
	State    string     `json:"state,omitempty"`
	NextWake *time.Time `json:"next_wake,omitempty"`
	Lead     bool       `json:"lead,omitempty"`
}

// GroupView is the read model for `pai group show` over a running server, which
// is the only way to inspect a group while `pai serve` owns the store.
type GroupView struct {
	Group    core.Group        `json:"group"`
	Members  []GroupMemberView `json:"members"`
	Tasks    []core.Task       `json:"tasks,omitempty"`
	Activity []GroupActivity   `json:"activity,omitempty"`
}

// GroupActivity is one row of a group's timeline: a message the host delivered to
// a member, or a report a member wrote back. It is a view, not a durable type:
// the events and turns it is built from are already stored.
type GroupActivity struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"` // mail, broadcast, user_input, escalation, inform, done
	// From is a session name, "user", or "group:<name>" for an escalation; To is
	// the recipient of a delivery and is empty for a report.
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Trace  string `json:"trace,omitempty"`
	State  string `json:"state,omitempty"` // delivery state, when the row is an event
	Detail string `json:"detail,omitempty"`
}

// Groups lists the group directory.
func (m *Manager) Groups() ([]core.Group, error) {
	groups, ok := m.backend.(GroupBackend)
	if !ok {
		return nil, errors.New("this host has no group directory")
	}
	return groups.Groups()
}

// CreateGroup writes a group from session names and returns the resulting view.
//
// Group membership is part of a resident worker's frozen prompt head (roster,
// shared goal, and the group tools), so every member's worker is marked for
// re-preparation: the next activation rebuilds its head from the directory
// instead of serving a stale one. That is why this can run while the server is
// up, which is the whole point of creating a group from the browser.
func (m *Manager) CreateGroup(name, goal, lead string, members []string, maxMembers int) (GroupView, error) {
	admin, ok := m.backend.(GroupAdminBackend)
	if !ok {
		return GroupView{}, errors.New("this host cannot create groups")
	}
	if err := admin.CreateGroup(name, goal, lead, members, maxMembers); err != nil {
		return GroupView{}, err
	}
	m.diag(Diagnostic{Session: name, Kind: "group", Outcome: "created",
		Detail: fmt.Sprintf("%d member(s)", len(members))})
	m.reloadGroupMembers(name)
	return m.GroupView(name)
}

// AddGroupMember adds one session and refreshes every member's prompt head: the
// roster is shared, so an addition changes what each of them reads.
func (m *Manager) AddGroupMember(group, session string) (GroupView, error) {
	admin, ok := m.backend.(GroupAdminBackend)
	if !ok {
		return GroupView{}, errors.New("this host cannot edit groups")
	}
	if err := admin.AddGroupMember(group, session); err != nil {
		return GroupView{}, err
	}
	m.diag(Diagnostic{Session: group, Kind: "group", Subject: session, Outcome: "joined"})
	m.reloadGroupMembers(group)
	return m.GroupView(group)
}

// RemoveGroupMember removes one membership. The lead is refused by the store,
// which keeps the group addressable.
func (m *Manager) RemoveGroupMember(group, session string) (GroupView, error) {
	admin, ok := m.backend.(GroupAdminBackend)
	if !ok {
		return GroupView{}, errors.New("this host cannot edit groups")
	}
	if err := admin.RemoveGroupMember(group, session); err != nil {
		return GroupView{}, err
	}
	m.diag(Diagnostic{Session: group, Kind: "group", Subject: session, Outcome: "left"})
	m.reloadGroupMembers(group)
	return m.GroupView(group)
}

// DeleteGroup removes the directory. The sessions keep running, but each one's
// next wake re-prepares without a roster or the group tools, so a deleted group
// does not linger in anybody's prompt.
func (m *Manager) DeleteGroup(name string) error {
	admin, ok := m.backend.(GroupAdminBackend)
	if !ok {
		return errors.New("this host cannot delete groups")
	}
	members := m.groupMemberNames(name)
	if err := admin.DeleteGroup(name); err != nil {
		return err
	}
	m.diag(Diagnostic{Session: name, Kind: "group", Outcome: "deleted"})
	for _, member := range members {
		m.markGroupDirty(member)
	}
	return nil
}

// groupMemberNames reads the roster before a delete, when the rows still exist.
func (m *Manager) groupMemberNames(name string) []string {
	groups, ok := m.backend.(GroupBackend)
	if !ok {
		return nil
	}
	g, found, err := groups.GroupByName(name)
	if err != nil || !found {
		return nil
	}
	members, err := groups.GroupMembers(g.ID)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(members))
	for _, mem := range members {
		names = append(names, mem.Name)
	}
	return names
}

// reloadGroupMembers marks every current member for re-preparation.
func (m *Manager) reloadGroupMembers(name string) {
	for _, member := range m.groupMemberNames(name) {
		m.markGroupDirty(member)
	}
}

// markGroupDirty asks a resident worker to rebuild its prompt head. The flag is
// consumed by Activate rather than acted on here: a worker that is mid-activation
// or holding a pending confirmation cannot be retired without losing work, and
// it must not be blocked from finishing either.
func (m *Manager) markGroupDirty(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.entries[name]; w != nil && w.mode == ModeService {
		w.groupDirty = true
	}
}

// Bounds for a group timeline read. The panel asks for the newest few and grows
// the window only when the reader asks for more; the underlying events are kept
// for 30 days, so this is about what one refresh reads, not retention.
const (
	groupActivityDefault = 10
	groupActivityMax     = 100
)

// GroupActivity is the read side of a group's timeline, for a panel that wants a
// different window than GroupView carries. A limit of 0 means the default; it is
// clamped to groupActivityMax.
func (m *Manager) GroupActivity(name string, limit int) ([]GroupActivity, error) {
	groups, ok := m.backend.(GroupBackend)
	if !ok {
		return nil, errors.New("this host has no group directory")
	}
	g, found, err := groups.GroupByName(name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	activity, ok := m.backend.(GroupActivityBackend)
	if !ok {
		return nil, nil
	}
	members, err := groups.GroupMembers(g.ID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = groupActivityDefault
	}
	if limit > groupActivityMax {
		limit = groupActivityMax
	}
	return activity.GroupActivity(members, limit)
}

// GroupView assembles a group, its roster with each member's persisted state,
// and the board. It reads persisted state only, so it works with no live worker.
func (m *Manager) GroupView(name string) (GroupView, error) {
	groups, ok := m.backend.(GroupBackend)
	if !ok {
		return GroupView{}, errors.New("this host has no group directory")
	}
	g, found, err := groups.GroupByName(name)
	if err != nil {
		return GroupView{}, err
	}
	if !found {
		return GroupView{}, ErrNotFound
	}
	members, err := groups.GroupMembers(g.ID)
	if err != nil {
		return GroupView{}, err
	}
	metas, _ := m.List()
	byID := make(map[string]Meta, len(metas))
	for _, meta := range metas {
		byID[meta.ID] = meta
	}
	view := GroupView{Group: g}
	for _, mem := range members {
		mv := GroupMemberView{ID: mem.SessionID, Name: mem.Name, Lead: mem.SessionID == g.LeadID}
		if meta, ok := byID[mem.SessionID]; ok {
			mv.Role, mv.State, mv.NextWake = meta.Role, meta.State, meta.NextWake
		}
		view.Members = append(view.Members, mv)
	}
	if tasks, ok := m.backend.(TaskBackend); ok {
		if list, err := tasks.Tasks(g.ID, nil, 50); err == nil {
			view.Tasks = list
		}
	}
	// A timeline is a convenience, like the board: a backend without one, or a
	// failed read, still returns the roster and the goal. The view carries the
	// newest few rows; `GroupActivity` serves a wider window on request.
	if activity, ok := m.backend.(GroupActivityBackend); ok {
		if list, err := activity.GroupActivity(members, groupActivityDefault); err == nil {
			view.Activity = list
		}
	}
	return view, nil
}

// groupBrief renders the session's team context for the prompt head: the group
// name, its shared goal, an optional lead, and the roster (which SetGroup sorts).
// A backend without a group directory, a missing membership, or a read error all
// mean "no group" — a directory lookup must never fail a session.
func (m *Manager) groupBrief(sessionID string) (chat.GroupBrief, bool) {
	groups, ok := m.backend.(GroupBackend)
	if !ok || sessionID == "" {
		return chat.GroupBrief{}, false
	}
	g, in, err := groups.GroupBySession(sessionID)
	if err != nil {
		m.diag(Diagnostic{Session: sessionID, Kind: "group", Outcome: "failed",
			Detail: fmt.Sprintf("read membership: %v", err)})
		return chat.GroupBrief{}, false
	}
	if !in {
		return chat.GroupBrief{}, false
	}
	members, err := groups.GroupMembers(g.ID)
	if err != nil {
		m.diag(Diagnostic{Session: sessionID, Kind: "group", Subject: g.Name, Outcome: "failed",
			Detail: fmt.Sprintf("read members: %v", err)})
		return chat.GroupBrief{}, false
	}
	brief := chat.GroupBrief{Name: g.Name, Goal: g.Goal, Lead: g.LeadName}
	for _, mem := range members {
		brief.Members = append(brief.Members, mem.Name)
	}
	return brief, true
}
