package runner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/core"
)

var errTaskMissing = errors.New("task not found")

// groupTestBackend is the service backend plus an in-memory group directory and
// wake counters, so the manager's optional ports are exercised the way the
// SQLite store implements them.
type groupTestBackend struct {
	*serviceTestBackend
	mu        sync.Mutex
	groups    map[string]core.Group
	bySession map[string]string
	members   map[string][]core.GroupMember

	// board is the in-memory task store; due/escalated model the sweep.
	tasks     map[string]core.Task
	due       []core.Task
	escalated map[string]bool
	taskSeq   int
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

// The board half of the backend. Semantics only need to be faithful enough to
// exercise the manager's group scoping and the escalation sweep.

func (b *groupTestBackend) CreateTask(t core.Task) (core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.taskSeq++
	t.ID = "t" + string(rune('0'+b.taskSeq))
	t.Status = core.TaskOpen
	t.Version = 1
	b.tasks[t.ID] = t
	return t, nil
}

func (b *groupTestBackend) Task(id string) (core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tasks[id]
	if !ok {
		return core.Task{}, errTaskMissing
	}
	return t, nil
}

func (b *groupTestBackend) Tasks(groupID string, statuses []core.TaskStatus, limit int) ([]core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []core.Task
	for _, t := range b.tasks {
		if t.GroupID != groupID {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func (b *groupTestBackend) ClaimTask(id, ownerID string, now time.Time, lease time.Duration) (core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tasks[id]
	if !ok {
		return core.Task{}, errTaskMissing
	}
	t.Status, t.OwnerID, t.Version = core.TaskClaimed, ownerID, t.Version+1
	t.LeaseUntil = now.Add(lease)
	b.tasks[id] = t
	return t, nil
}

func (b *groupTestBackend) UpdateTask(id, actorID string, expectVersion int, status core.TaskStatus, note string, allowForeign bool, now time.Time) (core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tasks[id]
	if !ok {
		return core.Task{}, errTaskMissing
	}
	if t.Version != expectVersion {
		return core.Task{}, errTaskMissing
	}
	t.Status, t.Version = status, t.Version+1
	b.tasks[id] = t
	return t, nil
}

func (b *groupTestBackend) ExpireTasks(now time.Time) ([]core.Task, error) { return nil, nil }
func (b *groupTestBackend) OrphanTasksOwnedBy(sessionID string, now time.Time) ([]core.Task, error) {
	return nil, nil
}

func (b *groupTestBackend) DueEscalations(groupID string, now time.Time) ([]core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []core.Task
	for _, t := range b.due {
		if t.GroupID == groupID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (b *groupTestBackend) MarkTaskEscalated(id string, kind core.TaskStatus, now time.Time) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.escalated == nil {
		b.escalated = make(map[string]bool)
	}
	if b.escalated[id] {
		return false, nil
	}
	b.escalated[id] = true
	return true, nil
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
		tasks:     map[string]core.Task{},
		escalated: map[string]bool{},
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

// A task id from another group is refused before any store write: membership is
// the board's boundary too, not just mail's.
func TestSessionTasksStayInTheGroup(t *testing.T) {
	m, backend := groupManager(t)
	backend.seed("hydro", "c1", [][2]string{{"c1", "coord"}, {"h1", "hydro"}})
	backend.seed("meteo", "m1", [][2]string{{"m1", "met"}})

	tasks := sessionTasks{m: m, from: "hydro"}
	task, err := tasks.Add("check the gate", "after the dive", 60)
	if err != nil {
		t.Fatal(err)
	}
	if task.GroupID != "g-hydro" || task.Title != "check the gate" {
		t.Fatalf("task = %+v", task)
	}

	foreign := core.Task{ID: "x1", GroupID: "g-meteo", Title: "theirs", Status: core.TaskOpen, Version: 1}
	backend.mu.Lock()
	backend.tasks["x1"] = foreign
	backend.mu.Unlock()
	if _, err := tasks.Claim("x1"); err == nil || !strings.Contains(err.Error(), "not on your board") {
		t.Fatalf("foreign claim = %v", err)
	}
	if _, err := tasks.Update("x1", 1, core.TaskDone, ""); err == nil || !strings.Contains(err.Error(), "not on your board") {
		t.Fatalf("foreign update = %v", err)
	}

	listed, err := tasks.List("", 10)
	if err != nil || len(listed) != 1 || listed[0].ID != task.ID {
		t.Fatalf("list = %+v %v", listed, err)
	}

	// A session outside any group has no board at all.
	if _, err := (sessionTasks{m: m, from: "solo"}).Add("x", "", 0); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("ungrouped add = %v", err)
	}
}

// The board sweep tells the lead once about stuck work and then stays quiet,
// even though the task is still stuck.
func TestCheckBoardEscalatesOnce(t *testing.T) {
	m, backend := groupManager(t)
	backend.seed("hydro", "c1", [][2]string{{"c1", "coord"}, {"h1", "hydro"}})
	m.mu.Lock()
	m.stoppedServices["coord"] = true
	m.mu.Unlock()
	backend.mu.Lock()
	backend.due = []core.Task{{
		ID: "t9", GroupID: "g-hydro", Title: "stuck", Status: core.TaskOrphaned,
	}}
	backend.mu.Unlock()

	w := &worker{m: m, name: "hydro", sessionID: "h1"}
	now := time.Now()
	m.checkBoard(w, now)
	events := backend.eventsSnapshot()
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one escalation", events)
	}
	if ev := events[0]; ev.Kind != "escalation" || ev.Session != "c1" || !strings.Contains(ev.Payload, "stuck") {
		t.Fatalf("escalation = %+v", ev)
	}

	m.checkBoard(w, now.Add(2*boardSweepInterval))
	if got := len(backend.eventsSnapshot()); got != 1 {
		t.Fatalf("escalated %d times, want once", got)
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
