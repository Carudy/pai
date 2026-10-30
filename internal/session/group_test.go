package session

import (
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/core"
)

func makeService(t *testing.T, s *sqliteStore, name string) string {
	t.Helper()
	sess, err := s.Create(Meta{Name: name, Cwd: "/w", Mode: ModeService})
	if err != nil {
		t.Fatal(err)
	}
	return sess.Meta.ID
}

// A group is a directory over existing sessions: creating, reading, and deleting
// it never touches a member's conversation, and membership is keyed by the
// immutable id so a rename cannot move it.
func TestGroupDirectoryRoundTrip(t *testing.T) {
	s := openSQLiteStore(t)
	lead := makeService(t, s, "coord")
	a := makeService(t, s, "hydro")
	b := makeService(t, s, "met")

	g := core.Group{Name: "hydro", LeadID: lead, MaxMembers: 3}
	if err := s.CreateGroup(g, []string{lead, a}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GroupByName("hydro")
	if err != nil || !ok {
		t.Fatalf("GroupByName = %v %v", ok, err)
	}
	if got.LeadName != "coord" || got.MaxMembers != 3 {
		t.Fatalf("group = %+v", got)
	}
	if _, ok, err := s.GroupBySession(a); err != nil || !ok {
		t.Fatalf("membership = %v %v", ok, err)
	}
	if _, ok, _ := s.GroupBySession(b); ok {
		t.Fatal("an ungrouped session reported a group")
	}
	members, err := s.GroupMembers(got.ID)
	if err != nil || len(members) != 2 {
		t.Fatalf("members = %+v, %v", members, err)
	}

	// One group per session: a second membership is refused, and so is adding a
	// session that already belongs elsewhere.
	if err := s.AddGroupMember(got.ID, b); err != nil {
		t.Fatal(err)
	}
	if err := s.AddGroupMember(got.ID, b); err != nil {
		t.Fatalf("re-adding a member should be a no-op: %v", err)
	}
	if err := s.CreateGroup(core.Group{Name: "other", LeadID: b}, []string{b}); err == nil {
		t.Fatal("a session joined two groups")
	}

	// The lead is the address for `group:<name>`, so it cannot leave silently.
	if err := s.RemoveGroupMember(got.ID, lead); err == nil {
		t.Fatal("the lead was removed")
	}
	if err := s.RemoveGroupMember(got.ID, a); err != nil {
		t.Fatal(err)
	}

	// Deleting a member session drops its membership (foreign key), and deleting
	// the group leaves the sessions alone.
	if err := s.Delete("met"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GroupBySession(b); ok {
		t.Fatal("a deleted session is still a member")
	}
	if err := s.DeleteGroup("hydro"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GroupByName("hydro"); ok {
		t.Fatal("the group survived its deletion")
	}
	if _, err := s.Get("hydro"); err != nil {
		t.Fatalf("deleting a group removed a member session: %v", err)
	}
}

func TestGroupRejectsAStrangerLead(t *testing.T) {
	s := openSQLiteStore(t)
	member := makeService(t, s, "a")
	lead := makeService(t, s, "b")
	err := s.CreateGroup(core.Group{Name: "g", LeadID: lead}, []string{member})
	if err == nil || !strings.Contains(err.Error(), "lead must be one of its members") {
		t.Fatalf("CreateGroup = %v, want a lead refusal", err)
	}
}
