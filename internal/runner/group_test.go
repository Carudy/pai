package runner

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/core"
)

// groupTestBackend is the service backend plus an in-memory group directory and
// wake counters, so the manager's optional ports are exercised the way the
// SQLite store implements them.
type groupTestBackend struct {
	*serviceTestBackend
	mu        sync.Mutex
	groups    map[string]core.Group
	bySession map[string]string
	members   map[string][]core.GroupMember
}

func (b *groupTestBackend) GroupByName(name string) (core.Group, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.groups[name]
	return g, ok, nil
}

func (b *groupTestBackend) GroupBySession(sessionID string) (core.Group, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	name, ok := b.bySession[sessionID]
	if !ok {
		return core.Group{}, false, nil
	}
	g, ok := b.groups[name]
	return g, ok, nil
}

func (b *groupTestBackend) GroupMembers(groupID string) ([]core.GroupMember, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]core.GroupMember(nil), b.members[groupID]...), nil
}

// seed registers a group and its members by immutable id.
func (b *groupTestBackend) seed(name, leadID string, members [][2]string) {
	g := core.Group{ID: "g-" + name, Name: name, LeadID: leadID, MaxMembers: 8}
	for _, m := range members {
		if m[0] == leadID {
			g.LeadName = m[1]
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.groups[name] = g
	for _, m := range members {
		b.bySession[m[0]] = name
		b.members[g.ID] = append(b.members[g.ID], core.GroupMember{GroupID: g.ID, SessionID: m[0], Name: m[1]})
	}
}

func groupManager(t *testing.T) (*Manager, *groupTestBackend) {
	t.Helper()
	backend := &groupTestBackend{
		serviceTestBackend: &serviceTestBackend{
			meta: Meta{Name: "svc", ID: "s1", Mode: ModeService, Cwd: t.TempDir(), CreatedAt: time.Now()},
		},
		groups:    map[string]core.Group{},
		bySession: map[string]string{},
		members:   map[string][]core.GroupMember{},
	}
	m := New(context.Background(), backend, 4)
	t.Cleanup(m.Close)
	m.mu.Lock()
	for name, id := range map[string]string{
		"coord": "c1", "hydro": "h1", "met": "m1", "svc": "s1", "lone": "l2", "solo": "s2",
	} {
		m.serviceNames[name] = true
		m.sessionIDs[name] = id
	}
	m.mu.Unlock()
	return m, backend
}

// Membership bounds delivery: two members of one group may talk, two ungrouped
// sessions keep the old behaviour, and nothing crosses a group edge in either
// direction.
func TestMailRespectsTheGroupBoundary(t *testing.T) {
	m, backend := groupManager(t)
	backend.seed("hydro", "c1", [][2]string{{"c1", "coord"}, {"h1", "hydro"}})
	backend.seed("meteo", "m1", [][2]string{{"m1", "met"}, {"s1", "svc"}})
	m.mu.Lock()
	for _, name := range []string{"coord", "hydro", "met", "svc", "lone", "solo"} {
		m.stoppedServices[name] = true
	}
	m.mu.Unlock()

	for _, tc := range []struct{ from, to, wantErr string }{
		{"coord", "hydro", ""},
		{"hydro", "coord", ""},
		{"coord", "met", "cross group edges"},
		{"coord", "svc", "cross group edges"},
		{"coord", "lone", "outside it"},
		{"lone", "coord", "only its members"},
		{"lone", "solo", ""},
	} {
		err := m.deliverMail(tc.from, tc.to, "hi", core.MailMeta{})
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s -> %s: %v", tc.from, tc.to, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s -> %s = %v, want %q", tc.from, tc.to, err, tc.wantErr)
		}
	}
}

// `group:<name>` addresses the lead, not every member: a group of N then has N
// addressed edges instead of N².
func TestGroupAddressReachesTheLead(t *testing.T) {
	m, backend := groupManager(t)
	backend.seed("hydro", "c1", [][2]string{{"c1", "coord"}, {"h1", "hydro"}})
	m.mu.Lock()
	m.stoppedServices["coord"] = true
	m.mu.Unlock()

	if err := m.deliverMail("hydro", "group:hydro", "status?", core.MailMeta{}); err != nil {
		t.Fatalf("group mail: %v", err)
	}
	events := backend.eventsSnapshot()
	if len(events) != 1 || events[0].Session != "c1" {
		t.Fatalf("events = %+v, want one on the lead", events)
	}

	if err := m.deliverMail("lone", "group:hydro", "hi", core.MailMeta{}); err == nil || !strings.Contains(err.Error(), "not a member") {
		t.Fatalf("non-member -> group = %v", err)
	}
	if err := m.deliverMail("coord", "group:hydro", "hi", core.MailMeta{}); err == nil || !strings.Contains(err.Error(), "no one to address") {
		t.Fatalf("lead -> own group = %v", err)
	}
	if err := m.deliverMail("hydro", "group:nope", "hi", core.MailMeta{}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unknown group = %v", err)
	}
}
