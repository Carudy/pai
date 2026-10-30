// Group types shared by the scheduler, the store, and the CLI. A group is a
// membership boundary, not an execution unit: sessions stay the unit that runs,
// and a group only decides who may address whom and how much a member may wake.
package core

// Group is one membership boundary. LeadID is a member session that receives
// `group:<name>` mail; MaxMembers bounds the roster, which is also what keeps the
// injected group block small.
type Group struct {
	ID         string
	Name       string
	LeadID     string
	LeadName   string // resolved for display; empty when the lead row is gone
	MaxMembers int
}

// GroupMember is one session's membership. Name is the session's display name,
// resolved on read so a listing needs no second query.
type GroupMember struct {
	GroupID   string
	SessionID string
	Name      string
}
