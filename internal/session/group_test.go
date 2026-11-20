package session

import (
	"errors"
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

// GroupFromNames is the one place a group definition written by a person becomes
// ids: the CLI manifest and the web form both call it, so "a member is a resident
// service" cannot mean two different things on the two surfaces.
func TestGroupFromNames(t *testing.T) {
	s := openSQLiteStore(t)
	svc := makeService(t, s, "hydro")
	if _, err := s.Create(Meta{Name: "chatty", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}

	g, ids, err := GroupFromNames(s, "river", "keep the gates safe", "hydro", []string{"hydro"}, 4)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if g.Name != "river" || g.Goal != "keep the gates safe" || g.LeadID != svc || g.MaxMembers != 4 {
		t.Fatalf("group = %+v", g)
	}
	if len(ids) != 1 || ids[0] != svc {
		t.Fatalf("ids = %v, want [%s]", ids, svc)
	}
	if err := s.CreateGroup(g, ids); err != nil {
		t.Fatalf("the resolved group did not persist: %v", err)
	}

	for _, tc := range []struct {
		lead    string
		members []string
		want    string
	}{
		{"", []string{"chatty"}, "chat session"},
		{"", []string{"ghost"}, "is not a session"},
		{"nobody", []string{"hydro"}, "lead"},
	} {
		if _, _, err := GroupFromNames(s, "bad", "", tc.lead, tc.members, 8); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: %v, want %q", tc.members, err, tc.want)
		}
	}
}

// A member's in-group description is per membership, cleared by an empty write,
// and refused when the member is not on the roster.
func TestGroupMemberIntroRoundTrip(t *testing.T) {
	s := openSQLiteStore(t)
	lead := makeService(t, s, "coord")
	hydro := makeService(t, s, "hydro")

	if err := s.CreateGroup(core.Group{Name: "hydro", LeadID: lead, MaxMembers: 4}, []string{lead, hydro}); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GroupByName("hydro")
	if err != nil || !found {
		t.Fatalf("group = %v %v", found, err)
	}

	if err := s.SetGroupMemberIntro(got.ID, hydro, "watches the river level"); err != nil {
		t.Fatal(err)
	}
	members, err := s.GroupMembers(got.ID)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]core.GroupMember{}
	for _, m := range members {
		byName[m.Name] = m
	}
	if byName["hydro"].Intro != "watches the river level" {
		t.Fatalf("members = %+v", members)
	}
	if byName["coord"].Intro != "" {
		t.Fatalf("an undescribed member gained one: %+v", byName["coord"])
	}

	if err := s.SetGroupMemberIntro(got.ID, hydro, ""); err != nil {
		t.Fatal(err)
	}
	members, _ = s.GroupMembers(got.ID)
	for _, m := range members {
		if m.Intro != "" {
			t.Fatalf("clearing left text: %+v", members)
		}
	}
	if err := s.SetGroupMemberIntro(got.ID, "nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown member = %v", err)
	}

	// ApplyGroupIntros resolves names against the roster it just wrote.
	if err := ApplyGroupIntros(s, "hydro", map[string]string{"coord": "owns the schedule"}); err != nil {
		t.Fatal(err)
	}
	if members, _ := s.GroupMembers(got.ID); members[0].Intro != "owns the schedule" {
		t.Fatalf("applied intro = %+v", members)
	}
	if err := ApplyGroupIntros(s, "hydro", map[string]string{"ghost": "x"}); err == nil {
		t.Fatal("a non-member was described")
	}
}
