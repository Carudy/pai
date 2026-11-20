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
	// idsBySession is the name→id table this double needs because the admin port
	// speaks names while the read port speaks ids, exactly as the store does.
	idsBySession map[string]string
	// lastActivityLimit records the window the manager asked for, so a test can
	// check the default and the clamp without a real store.
	lastActivityLimit int

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

func (b *groupTestBackend) Groups() ([]core.Group, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]core.Group, 0, len(b.groups))
	for _, g := range b.groups {
		out = append(out, g)
	}
	return out, nil
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

// Delete lets the manager's delete path run; the real store drops the row and the
// cascade with it, which no runner test needs to model.
func (b *groupTestBackend) Delete(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.groups, name)
	return nil
}

// The write half of the directory, kept faithful enough that the manager's
// post-write read (GroupView) sees what was written.
func (b *groupTestBackend) CreateGroup(name, goal, lead string, members []string, maxMembers int) error {
	b.mu.Lock()
	_, exists := b.groups[name]
	b.mu.Unlock()
	if exists {
		return errors.New("group already exists")
	}
	pairs := make([][2]string, 0, len(members))
	for _, member := range members {
		pairs = append(pairs, [2]string{b.idsBySession[member], member})
	}
	b.seed(name, b.idsBySession[lead], pairs)
	b.mu.Lock()
	g := b.groups[name]
	g.Goal, g.MaxMembers, g.Members = goal, maxMembers, len(pairs)
	b.groups[name] = g
	b.mu.Unlock()
	return nil
}

func (b *groupTestBackend) AddGroupMember(group, member string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.groups[group]
	if !ok {
		return errors.New("group not found")
	}
	id := b.idsBySession[member]
	if id == "" {
		return errors.New("session not found")
	}
	for _, mem := range b.members[g.ID] {
		if mem.SessionID == id {
			return nil
		}
	}
	b.bySession[id] = group
	b.members[g.ID] = append(b.members[g.ID], core.GroupMember{GroupID: g.ID, SessionID: id, Name: member})
	g.Members = len(b.members[g.ID])
	b.groups[group] = g
	return nil
}

func (b *groupTestBackend) RemoveGroupMember(group, member string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.groups[group]
	if !ok {
		return errors.New("group not found")
	}
	id := b.idsBySession[member]
	if g.LeadID == id {
		return errors.New("the lead cannot be removed")
	}
	kept := b.members[g.ID][:0]
	for _, mem := range b.members[g.ID] {
		if mem.SessionID != id {
			kept = append(kept, mem)
		}
	}
	b.members[g.ID] = kept
	delete(b.bySession, id)
	g.Members = len(kept)
	b.groups[group] = g
	return nil
}

func (b *groupTestBackend) SetGroupMemberIntro(group, member, intro string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.groups[group]
	if !ok {
		return errors.New("group not found")
	}
	id := b.idsBySession[member]
	list := b.members[g.ID]
	for i := range list {
		if list[i].SessionID == id {
			list[i].Intro = intro
			b.members[g.ID] = list
			return nil
		}
	}
	return errors.New("not a member")
}

func (b *groupTestBackend) DeleteGroup(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.groups[name]
	if !ok {
		return errors.New("group not found")
	}
	for _, mem := range b.members[g.ID] {
		delete(b.bySession, mem.SessionID)
	}
	delete(b.members, g.ID)
	delete(b.groups, name)
	return nil
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

// A directed pair is quarantined once it exceeds the window's limit. The refusal
// is structural: the model cannot talk its way past it, and it is reported once
// per episode so a loop cannot turn its own diagnostics into load.
func TestMailQuarantineStopsAPair(t *testing.T) {
	m, _ := groupManager(t)
	m.mu.Lock()
	m.stoppedServices["lone"] = true
	m.stoppedServices["solo"] = true
	m.mu.Unlock()
	diags, unsubscribe, err := m.SubscribeDiagnostics()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	for i := 0; i < maxPerPairPerWindow; i++ {
		if _, err := m.deliverMail("lone", "solo", "tick", core.MailMeta{}); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	if _, err := m.deliverMail("lone", "solo", "one too many", core.MailMeta{}); err == nil || !strings.Contains(err.Error(), "too many messages") {
		t.Fatalf("quarantine = %v", err)
	}
	// The reverse direction is a different directed pair and stays open.
	if _, err := m.deliverMail("solo", "lone", "hi", core.MailMeta{}); err != nil {
		t.Fatalf("reverse direction: %v", err)
	}
	// Repeating a refused send does not repeat the diagnostic.
	if _, err := m.deliverMail("lone", "solo", "again", core.MailMeta{}); err == nil {
		t.Fatal("the pair stayed open")
	}
	refusals := 0
	for _, d := range drainDiagnostics(diags) {
		if d.Kind == "mail" && d.Outcome == "refused" && d.Subject == "solo" && strings.Contains(d.Detail, "more than") {
			refusals++
		}
	}
	if refusals != 1 {
		t.Fatalf("refusal diagnostics = %d, want 1", refusals)
	}

	// The window is wall-clock, so age the entry rather than sleeping: the pair
	// must become reachable again without a restart.
	m.quarantineMu.Lock()
	for _, e := range m.quarantine {
		for i := range e.times {
			e.times[i] = time.Now().Add(-quarantineWindow - time.Minute)
		}
	}
	m.quarantineMu.Unlock()
	if _, err := m.deliverMail("lone", "solo", "after the window", core.MailMeta{}); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

// A broadcast is admitted per recipient, so a large group cannot be used to send
// more to one peer than an addressed message would allow. Nothing is counted on
// a refusal, so the batch is all-or-nothing like a full mailbox.
func TestBroadcastCountsEveryRecipient(t *testing.T) {
	m, backend := groupManager(t)
	backend.seed("hydro", "c1", [][2]string{{"c1", "coord"}, {"h1", "hydro"}, {"m1", "met"}})
	m.mu.Lock()
	for _, name := range []string{"coord", "hydro", "met"} {
		m.stoppedServices[name] = true
	}
	m.mu.Unlock()

	// Fill the hydro→met pair with addressed mail, then try one broadcast: it would
	// push that pair over the limit, so the whole send is refused before anything
	// is queued for the other member either.
	for i := 0; i < maxPerPairPerWindow; i++ {
		if _, err := m.deliverMail("hydro", "met", "tick", core.MailMeta{}); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	if _, err := m.deliverMail("hydro", "group:hydro", "status", core.MailMeta{}); err == nil || !strings.Contains(err.Error(), "too many messages") {
		t.Fatalf("broadcast quarantine = %v", err)
	}
}

// Leading a group is an address the other members still hold, so the delete is
// refused rather than leaving a dangling lead behind.
func TestDeleteRefusesAGroupLead(t *testing.T) {
	m, backend := groupManager(t)
	backend.seed("hydro", "c1", [][2]string{{"c1", "coord"}, {"h1", "hydro"}})
	if err := m.Delete("coord"); err == nil || !strings.Contains(err.Error(), "leads group") {
		t.Fatalf("delete lead = %v", err)
	}
	if err := m.Delete("hydro"); err != nil {
		t.Fatalf("delete a member: %v", err)
	}
}

// A group write made while the server runs must reach the members that are
// already prepared: their prompt head (roster, shared goal, group tools) is
// frozen, so the worker is marked and Activate rebuilds it before the next wake.
func TestGroupWriteRefreshesPreparedMembers(t *testing.T) {
	m, backend := groupManager(t)
	idle := &worker{mode: ModeService, state: StateSuspended, done: make(chan struct{})}
	var once sync.Once
	idle.cancel = func() { once.Do(func() { close(idle.done) }) }
	m.mu.Lock()
	m.entries["coord"] = idle
	m.mu.Unlock()
	t.Cleanup(func() {
		m.mu.Lock()
		delete(m.entries, "coord")
		m.mu.Unlock()
	})
	diags, unsubscribe, err := m.SubscribeDiagnostics()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	view, err := m.CreateGroup("hydro", "keep the gates safe", "coord", []string{"coord", "hydro"}, 8)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if view.Group.Name != "hydro" || len(view.Members) != 2 || view.Group.Goal != "keep the gates safe" {
		t.Fatalf("view = %+v", view)
	}
	if !idle.groupDirty {
		t.Fatal("the prepared member was not marked for a reload")
	}

	m.recycleGroupWorker("coord")
	if idle.groupDirty || idle.state != StateStopped {
		t.Fatalf("worker after recycle = dirty %v state %q", idle.groupDirty, idle.state)
	}
	if findDiagnostic(drainDiagnostics(diags), func(d Diagnostic) bool {
		return d.Kind == "group" && d.Outcome == "reloaded" && d.Session == "coord"
	}) == nil {
		t.Fatal("no reload diagnostic")
	}

	// A parked worker holds a question nobody else can answer; it keeps the flag
	// and is left alone until a later wake.
	parked := &worker{mode: ModeService, state: StateParked, parked: true, groupDirty: true, done: make(chan struct{})}
	parked.cancel = func() { t.Fatal("a parked worker must not be retired") }
	m.mu.Lock()
	m.entries["hydro"] = parked
	m.mu.Unlock()
	t.Cleanup(func() {
		m.mu.Lock()
		delete(m.entries, "hydro")
		m.mu.Unlock()
	})
	m.recycleGroupWorker("hydro")
	if !parked.groupDirty || parked.state != StateParked {
		t.Fatal("a parked worker lost its flag or its state")
	}

	// Adding and removing also mark the roster, so every member rebuilds.
	idle.groupDirty = false
	if _, err := m.AddGroupMember("hydro", "met"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if !idle.groupDirty {
		t.Fatal("adding a member did not mark the others")
	}
	idle.groupDirty = false
	if _, err := m.RemoveGroupMember("hydro", "met"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !idle.groupDirty {
		t.Fatal("removing a member did not mark the others")
	}
	if _, err := m.RemoveGroupMember("hydro", "coord"); err == nil {
		t.Fatal("the lead was removed")
	}
	if err := m.DeleteGroup("hydro"); err != nil {
		t.Fatalf("delete group: %v", err)
	}
	if _, found, _ := backend.GroupByName("hydro"); found {
		t.Fatal("the group survived the delete")
	}
}

func (b *groupTestBackend) GroupActivity(members []core.GroupMember, limit int) ([]GroupActivity, error) {
	b.mu.Lock()
	b.lastActivityLimit = limit
	b.mu.Unlock()
	if len(members) == 0 {
		return nil, nil
	}
	return []GroupActivity{{Kind: "mail", From: members[0].Name, To: members[len(members)-1].Name, Detail: "gate schedule final?"}}, nil
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
	identities := map[string]string{
		"coord": "c1", "hydro": "h1", "met": "m1", "svc": "s1", "lone": "l2", "solo": "s2",
	}
	backend := &groupTestBackend{
		serviceTestBackend: &serviceTestBackend{
			meta: Meta{Name: "svc", ID: "s1", Mode: ModeService, Cwd: t.TempDir(), CreatedAt: time.Now()},
		},
		groups:       map[string]core.Group{},
		bySession:    map[string]string{},
		members:      map[string][]core.GroupMember{},
		idsBySession: identities,
		tasks:        map[string]core.Task{},
		escalated:    map[string]bool{},
	}
	m := New(context.Background(), backend, 4)
	t.Cleanup(m.Close)
	m.mu.Lock()
	for name, id := range identities {
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

// GroupView is what `GET /api/group` serves, so it must carry the roster with
// each member's persisted state and the board in one response.
func TestGroupViewForTheServer(t *testing.T) {
	m, backend := groupManager(t)
	backend.seed("hydro", "c1", [][2]string{{"c1", "coord"}, {"h1", "hydro"}})
	backend.mu.Lock()
	backend.tasks["t1"] = core.Task{ID: "t1", GroupID: "g-hydro", Title: "check the gate", Status: core.TaskOpen, Version: 1}
	backend.mu.Unlock()

	view, err := m.GroupView("hydro")
	if err != nil {
		t.Fatal(err)
	}
	if view.Group.Name != "hydro" || len(view.Members) != 2 || len(view.Tasks) != 1 {
		t.Fatalf("view = %+v", view)
	}
	// The timeline is part of the same response: the panel does not make a second
	// round trip just to show what the team has been doing.
	if len(view.Activity) != 1 || view.Activity[0].Kind != "mail" {
		t.Fatalf("activity = %+v", view.Activity)
	}
	lead := false
	for _, mem := range view.Members {
		if mem.Name == "coord" && mem.Lead {
			lead = true
		}
	}
	if !lead {
		t.Fatalf("the lead flag is missing: %+v", view.Members)
	}
	if _, err := m.GroupView("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing group = %v", err)
	}
}

func TestGroupBriefRoster(t *testing.T) {
	m, backend := groupManager(t)
	backend.seed("hydro", "c1", [][2]string{{"c1", "coord"}, {"h1", "hydro"}})
	if err := backend.SetGroupMemberIntro("hydro", "hydro", "watches the river level"); err != nil {
		t.Fatal(err)
	}
	brief, ok := m.groupBrief("h1")
	if !ok {
		t.Fatal("a member got no brief")
	}
	if brief.Name != "hydro" || brief.Lead != "coord" || len(brief.Members) != 2 {
		t.Fatalf("brief = %+v", brief)
	}
	// The description rides the brief; the member without one is absent, so the
	// prompt renders no empty entry.
	if brief.Roles["hydro"] != "watches the river level" || len(brief.Roles) != 1 {
		t.Fatalf("roles = %+v", brief.Roles)
	}
	if _, ok := m.groupBrief("l2"); ok {
		t.Fatal("an ungrouped session got a brief")
	}
}

// Starting or stopping a group counts only real transitions, so pressing Start on
// a running group is not a burst of immediate probes; a member that refuses is
// named and does not stop the others.
func TestGroupStartStopCountsTransitions(t *testing.T) {
	m, backend := groupManager(t)
	backend.seed("hydro", "c1", [][2]string{{"c1", "coord"}, {"h1", "hydro"}})
	m.mu.Lock()
	m.stoppedServices["coord"] = true
	m.stoppedServices["hydro"] = true
	m.mu.Unlock()

	changed, failed, err := m.StartGroup("hydro")
	if err != nil || changed != 2 || len(failed) != 0 {
		t.Fatalf("start = %d %v %v", changed, failed, err)
	}
	if changed, _, _ := m.StartGroup("hydro"); changed != 0 {
		t.Fatalf("a second start changed %d member(s)", changed)
	}
	changed, failed, err = m.StopGroup("hydro")
	if err != nil || changed != 2 || len(failed) != 0 {
		t.Fatalf("stop = %d %v %v", changed, failed, err)
	}
	if changed, _, _ := m.StopGroup("hydro"); changed != 0 {
		t.Fatalf("a second stop changed %d member(s)", changed)
	}
	if _, _, err := m.StartGroup("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing group = %v", err)
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

// The sidebar renders /api/sessions, and the store only knows the state an
// activation ended in. A live worker's state must win, otherwise every row reads
// "idle" while the session is visibly working.
func TestListReportsTheLiveState(t *testing.T) {
	m, backend := groupManager(t)
	backend.meta.State = StateSuspended

	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].State != StateSuspended {
		t.Fatalf("list = %+v, want the persisted state with no worker", list)
	}

	w := &worker{m: m, name: "svc", mode: ModeService, state: StateBusy, done: make(chan struct{})}
	w.cancel = func() {}
	m.mu.Lock()
	m.entries["svc"] = w
	m.mu.Unlock()
	t.Cleanup(func() {
		m.mu.Lock()
		delete(m.entries, "svc")
		m.mu.Unlock()
	})

	list, err = m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].State != StateBusy {
		t.Fatalf("list = %+v, want the live busy state", list)
	}
}

// A timeline read is bounded: the panel asks for a few rows and grows the window
// on demand, so an unset limit is the default and a large one is clamped.
func TestGroupActivityWindowIsBounded(t *testing.T) {
	m, backend := groupManager(t)
	backend.seed("hydro", "c1", [][2]string{{"c1", "coord"}, {"h1", "hydro"}})

	if _, err := m.GroupActivity("hydro", 0); err != nil {
		t.Fatal(err)
	}
	if backend.lastActivityLimit != groupActivityDefault {
		t.Fatalf("limit = %d, want the default %d", backend.lastActivityLimit, groupActivityDefault)
	}
	if _, err := m.GroupActivity("hydro", 5000); err != nil {
		t.Fatal(err)
	}
	if backend.lastActivityLimit != groupActivityMax {
		t.Fatalf("limit = %d, want the cap %d", backend.lastActivityLimit, groupActivityMax)
	}
	if _, err := m.GroupActivity("nope", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing group = %v", err)
	}
}
