package runner

import (
	"fmt"

	"github.com/Carudy/pai/internal/chat"
)

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
