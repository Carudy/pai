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
	Group   core.Group        `json:"group"`
	Members []GroupMemberView `json:"members"`
	Tasks   []core.Task       `json:"tasks,omitempty"`
}

// Groups lists the group directory.
func (m *Manager) Groups() ([]core.Group, error) {
	groups, ok := m.backend.(GroupBackend)
	if !ok {
		return nil, errors.New("this host has no group directory")
	}
	return groups.Groups()
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
