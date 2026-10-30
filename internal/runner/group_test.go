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
		_, err := m.deliverMail(tc.from, tc.to, "hi", core.MailMeta{})
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s -> %s: %v", tc.from, tc.to, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s -> %s = %v, want %q", tc.from, tc.to, err, tc.wantErr)
		}
	}
}

// `group:<name>` fans out to every other member, one durable event each, with
// the sender excluded. Addressed mail stays the narrow path.
func TestGroupAddressBroadcasts(t *testing.T) {
	m, backend := groupManager(t)
	backend.seed("hydro", "c1", [][2]string{{"c1", "coord"}, {"h1", "hydro"}, {"m1", "met"}})
	m.mu.Lock()
	m.stoppedServices["coord"] = true
	m.stoppedServices["met"] = true
	m.mu.Unlock()

	sent, err := m.deliverMail("hydro", "group:hydro", "status?", core.MailMeta{})
	if err != nil {
		t.Fatalf("group mail: %v", err)
	}
	if sent != 2 {
		t.Fatalf("recipients = %d, want 2", sent)
	}
	events := backend.eventsSnapshot()
	if len(events) != 2 {
		t.Fatalf("events = %+v, want one per other member", events)
	}
	seen := map[string]bool{}
	for _, ev := range events {
		if ev.Kind != "broadcast" || ev.Class != core.EventPeer {
			t.Fatalf("event = %+v, want a peer broadcast", ev)
		}
		if ev.Session == "h1" {
			t.Fatal("the sender received its own broadcast")
		}
		seen[ev.Session] = true
	}
	if !seen["c1"] || !seen["m1"] {
		t.Fatalf("recipients = %v", seen)
	}

	if _, err := m.deliverMail("lone", "group:hydro", "hi", core.MailMeta{}); err == nil || !strings.Contains(err.Error(), "not a member") {
		t.Fatalf("non-member -> group = %v", err)
	}
	if _, err := m.deliverMail("hydro", "group:nope", "hi", core.MailMeta{}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unknown group = %v", err)
	}
}

// A grouped session's head carries the team: its name, shared goal, lead, and
// the roster it can address, and `mail` is added by the session layer even when
// the role does not declare it.
func TestGroupedServicePromptCarriesTheGroup(t *testing.T) {
	fp := &serviceTestProvider{
		replies: []string{`{"action":"yield","payload":{},"reason":"ok"}`},
		done:    make(chan int, 4),
	}
	m, backend := groupManager(t)
	backend.provider = fp
	// The coder role does not declare mail: the tool must come from the session
	// layer, because this session is in a group.
	backend.role = "coder"
	backend.seed("ops", "c1", [][2]string{{"c1", "coord"}, {"s1", "svc"}})
	backend.mu.Lock()
	g := backend.groups["ops"]
	g.Goal = "watch the river while the dam is serviced"
	backend.groups["ops"] = g
	backend.mu.Unlock()

	if err := m.Send("svc", "check now"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the grouped activation", func() bool { return fp.CallCount() == 1 })

	head := fp.last.Messages[0]
	for _, want := range []string{
		"## Group: ops",
		"Shared goal: watch the river while the dam is serviced",
		"Members: coord (lead), svc.",
		"## Tool: mail",
	} {
		if !strings.Contains(head.Content, want) {
			t.Errorf("head is missing %q:\n%s", want, head.Content)
		}
	}
}

func TestGroupBriefRoster(t *testing.T) {
	m, backend := groupManager(t)
	backend.seed("hydro", "c1", [][2]string{{"c1", "coord"}, {"h1", "hydro"}})
	brief, ok := m.groupBrief("h1")
	if !ok {
		t.Fatal("a member got no brief")
	}
	if brief.Name != "hydro" || brief.Lead != "coord" || len(brief.Members) != 2 {
		t.Fatalf("brief = %+v", brief)
	}
	if _, ok := m.groupBrief("l2"); ok {
		t.Fatal("an ungrouped session got a brief")
	}
}

// A group of one has nobody to tell, and a broadcast burst is rate limited per
// sender rather than by a wake budget.
func TestBroadcastLimits(t *testing.T) {
	m, backend := groupManager(t)
	backend.seed("solo", "s2", [][2]string{{"s2", "solo"}})
	if _, err := m.deliverMail("solo", "group:solo", "hi", core.MailMeta{}); err == nil || !strings.Contains(err.Error(), "no other members") {
		t.Fatalf("lonely group = %v", err)
	}

	backend.seed("pair", "c1", [][2]string{{"c1", "coord"}, {"l2", "lone"}})
	m.mu.Lock()
	m.stoppedServices["lone"] = true
	m.mu.Unlock()
	for i := 0; i < maxBroadcastsPerWindow; i++ {
		if _, err := m.deliverMail("coord", "group:pair", "tick", core.MailMeta{}); err != nil {
			t.Fatalf("broadcast %d: %v", i, err)
		}
	}
	if _, err := m.deliverMail("coord", "group:pair", "one too many", core.MailMeta{}); err == nil || !strings.Contains(err.Error(), "too many group broadcasts") {
		t.Fatalf("rate limit = %v", err)
	}
}
