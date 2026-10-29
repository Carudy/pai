package session

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/core"
)

func TestCreateAssignsImmutableID(t *testing.T) {
	s := openSQLiteStore(t)
	created, err := s.Create(Meta{Name: "svc", Role: "devops", Model: "test:model", Cwd: "/w", Mode: "service"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Meta.ID == "" || created.Meta.Mode != "service" {
		t.Fatalf("created = %+v", created.Meta)
	}
	got, err := s.Get("svc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Meta.ID != created.Meta.ID || got.Meta.Mode != "service" {
		t.Fatalf("stored = %+v, want the created id/mode", got.Meta)
	}
	metas, err := s.List()
	if err != nil || len(metas) != 1 || metas[0].ID != created.Meta.ID {
		t.Fatalf("list = %+v, %v", metas, err)
	}

	other, err := s.Create(Meta{Name: "chat", Role: "devops", Model: "test:model"})
	if err != nil {
		t.Fatal(err)
	}
	if other.Meta.ID == "" || other.Meta.ID == created.Meta.ID {
		t.Fatalf("ids are not unique: %q %q", created.Meta.ID, other.Meta.ID)
	}
	if other.Meta.Mode != "chat" {
		t.Fatalf("default mode = %q, want chat", other.Meta.Mode)
	}
}

func TestTurnEventIDIsDurable(t *testing.T) {
	s := openSQLiteStore(t)
	if _, err := s.Create(Meta{Name: "svc", Role: "devops", Model: "test:model", Cwd: "/w", Mode: "service"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("svc", core.Turn{Role: "user", Kind: core.KindInput, Content: "check", EventID: 42}); err != nil {
		t.Fatal(err)
	}
	ok, err := s.HasTurnEvent("svc", 42)
	if err != nil || !ok {
		t.Fatalf("HasTurnEvent = %v, %v; want true", ok, err)
	}
	ok, err = s.HasTurnEvent("svc", 99)
	if err != nil || ok {
		t.Fatalf("HasTurnEvent(99) = %v, %v; want false", ok, err)
	}
	got, err := s.Get("svc")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 1 || got.Turns[0].EventID != 42 {
		t.Fatalf("turns = %+v", got.Turns)
	}
}

// A database created before the identity columns existed gains them on open, and
// the backfilled id is stable across reopens.
func TestOldDatabaseGainsIdentityAndServiceColumns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
CREATE TABLE sessions (
  name TEXT PRIMARY KEY, role TEXT NOT NULL, model TEXT NOT NULL, cwd TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
INSERT INTO sessions(name, role, model, cwd, created_at, updated_at)
VALUES('one', 'devops', 'test:model', '/w', 1, 1), ('two', 'coder', 'test:model', '/w', 1, 1);`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := openBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Get("one")
	if err != nil {
		t.Fatalf("old database did not open: %v", err)
	}
	second, err := store.Get("two")
	if err != nil {
		t.Fatal(err)
	}
	if first.Meta.ID == "" || second.Meta.ID == "" || first.Meta.ID == second.Meta.ID {
		t.Fatalf("backfilled ids = %q, %q", first.Meta.ID, second.Meta.ID)
	}
	if first.Meta.Mode != "chat" || first.Meta.State != "" || first.Meta.NextWake != nil {
		t.Fatalf("new columns = %+v", first.Meta)
	}
	id := first.Meta.ID
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := openBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := reopened.Get("one")
	if err != nil {
		t.Fatal(err)
	}
	if again.Meta.ID != id {
		t.Fatalf("id changed across reopen: %q -> %q", id, again.Meta.ID)
	}
}

func TestOldEventsGainPriority(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
CREATE TABLE sessions (
  name TEXT PRIMARY KEY, id TEXT, role TEXT NOT NULL, model TEXT NOT NULL, cwd TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  prompt_tokens INTEGER NOT NULL DEFAULT 0, prompt_estimate INTEGER NOT NULL DEFAULT 0,
  mode TEXT NOT NULL DEFAULT 'chat', state TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '', spec TEXT NOT NULL DEFAULT '{}', next_wake INTEGER
);
CREATE UNIQUE INDEX sessions_id ON sessions(id);
CREATE TABLE events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, class INTEGER NOT NULL,
  kind TEXT NOT NULL, source TEXT NOT NULL, payload TEXT NOT NULL, dedup_key TEXT,
  created_at INTEGER NOT NULL, available_at INTEGER NOT NULL, deadline INTEGER,
  attempts INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL DEFAULT 'pending',
  lease_until INTEGER, last_error TEXT
);
INSERT INTO sessions(name,id,role,model,cwd,created_at,updated_at)
VALUES('svc','sid','devops','test:model','/w',1,1);
INSERT INTO events(session_id,class,kind,source,payload,created_at,available_at)
VALUES ('sid',0,'x','test','safety',1,1), ('sid',7,'x','test','telemetry',1,1);
PRAGMA user_version = 1;`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := openBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created, err := store.Get("svc")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := store.(ServiceStore).QueuedEvents(created.Meta.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 2 || queued[0].Priority != 100 || queued[1].Priority != 10 {
		t.Fatalf("backfilled queue = %+v, want safety 100 then telemetry 10", queued)
	}
}

func TestEventsDedupLeaseAndAck(t *testing.T) {
	s := openSQLiteStore(t)
	created, err := s.Create(Meta{Name: "svc", Role: "devops", Model: "test:model", Mode: "service"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.ServiceInfo("svc")
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != created.Meta.ID {
		t.Fatalf("service id = %q, want %q", info.ID, created.Meta.ID)
	}

	base := time.Now().Truncate(time.Second)
	first, err := s.AppendEvent(core.Event{
		Session: info.ID, Class: core.EventWatcher, Kind: "watch",
		Source: "watch:river", Payload: "level=1", DedupKey: "watch:river", At: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AppendEvent(core.Event{
		Session: info.ID, Class: core.EventWatcher, Kind: "watch",
		Source: "watch:river", Payload: "level=2", DedupKey: "watch:river", At: base.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("dedup insert = %d, want the pending event %d", second, first)
	}
	due, err := s.DueEvents(info.ID, base.Add(2*time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Payload != "level=2" || due[0].Class != core.EventWatcher || due[0].DedupKey != "watch:river" {
		t.Fatalf("due = %+v, want the replaced pending event", due)
	}

	if err := s.LeaseEvents([]int64{first}, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if due, err := s.DueEvents(info.ID, base.Add(2*time.Second), 10); err != nil || len(due) != 0 {
		t.Fatalf("leased event is still due: %+v, %v", due, err)
	}
	if n, err := s.PendingEvents(info.ID); err != nil || n != 1 {
		t.Fatalf("pending = %d, %v; a leased event is still in flight", n, err)
	}

	if err := s.ResetLeases(); err != nil {
		t.Fatal(err)
	}
	if due, err := s.DueEvents(info.ID, base.Add(2*time.Second), 10); err != nil || len(due) != 1 {
		t.Fatalf("reset lease not re-deliverable: %+v, %v", due, err)
	}
	if err := s.AckEvents([]int64{first}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PendingEvents(info.ID); err != nil || n != 0 {
		t.Fatalf("acked event still pending: %d, %v", n, err)
	}
}

func TestDueEventsOrdersByClassThenDeadline(t *testing.T) {
	s := openSQLiteStore(t)
	created, err := s.Create(Meta{Name: "svc", Role: "devops", Model: "test:model", Mode: "service"})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Truncate(time.Second).Add(-time.Hour)
	appendEvent := func(class core.EventClass, deadline time.Time, payload string) int64 {
		t.Helper()
		id, err := s.AppendEvent(core.Event{
			Session: created.Meta.ID, Class: class, Kind: "test", Source: "test",
			Payload: payload, At: base, Deadline: deadline,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	timer := appendEvent(core.EventTimer, time.Time{}, "timer")
	user := appendEvent(core.EventUser, base.Add(time.Hour), "user")
	deadline := appendEvent(core.EventUser, base.Add(time.Minute), "deadline")
	watcher := appendEvent(core.EventWatcher, time.Time{}, "watcher")

	due, err := s.DueEvents(created.Meta.ID, time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]int64, len(due))
	for i, ev := range due {
		got[i] = ev.ID
	}
	want := []int64{deadline, user, watcher, timer}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestQueuedEventsShowsWaitingWork(t *testing.T) {
	s := openSQLiteStore(t)
	created, err := s.Create(Meta{Name: "q", Role: "devops", Model: "test:model", Mode: "service"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	watcher, err := s.AppendEvent(core.Event{
		Session: created.Meta.ID, Class: core.EventWatcher, Kind: "watch",
		Source: "watch:a", Payload: "due", At: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	mail, err := s.AppendEvent(core.Event{
		Session: created.Meta.ID, Class: core.EventPeer, Kind: "mail",
		Source: "session:x", Payload: "later", At: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LeaseEvents([]int64{watcher}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.NackEvent(mail, now.Add(time.Hour), "retry later", false); err != nil {
		t.Fatal(err)
	}

	// Neither event is deliverable now: one is leased, the other is waiting for
	// its retry time. The operator view still lists both, most urgent first.
	if due, err := s.DueEvents(created.Meta.ID, now, 10); err != nil || len(due) != 0 {
		t.Fatalf("due = %+v, %v", due, err)
	}
	queued, err := s.QueuedEvents(created.Meta.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 2 || queued[0].ID != mail || queued[1].ID != watcher {
		t.Fatalf("queued = %+v, want the peer before the watcher", queued)
	}
	if queued[0].Attempts != 1 {
		t.Fatalf("attempts = %d, want the nack counted", queued[0].Attempts)
	}

	// The nacked mail defines the next wake; the leased watcher is in flight and
	// must not schedule a second activation.
	at, ok, err := s.NextEventAt(created.Meta.ID)
	if err != nil || !ok || at.Unix() != now.Add(time.Hour).Unix() {
		t.Fatalf("next event = %v, %v, %v; want the mail retry", at, ok, err)
	}
	if err := s.AckEvents([]int64{mail}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.NextEventAt(created.Meta.ID); err != nil || ok {
		t.Fatalf("next event after ack = %v, %v; want none", ok, err)
	}
}

func TestOldDoneEventsArePruned(t *testing.T) {
	s := openSQLiteStore(t)
	created, err := s.Create(Meta{Name: "svc", Role: "devops", Model: "test:model", Mode: "service"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.AppendEvent(core.Event{Session: created.Meta.ID, Class: core.EventUser, Kind: "test", Source: "user", At: time.Now().Add(-31 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AckEvents([]int64{id}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM events WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("old done event was not pruned: %d rows", n)
	}
}

func TestNackRetriesThenDies(t *testing.T) {
	s := openSQLiteStore(t)
	created, err := s.Create(Meta{Name: "svc", Role: "devops", Model: "test:model", Mode: "service"})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Truncate(time.Second)
	id, err := s.AppendEvent(core.Event{Session: created.Meta.ID, Class: core.EventUser, Kind: "test", Source: "user", At: base})
	if err != nil {
		t.Fatal(err)
	}
	retry := base.Add(time.Minute)
	if err := s.NackEvent(id, retry, "temporary", false); err != nil {
		t.Fatal(err)
	}
	if due, err := s.DueEvents(created.Meta.ID, base.Add(time.Second), 10); err != nil || len(due) != 0 {
		t.Fatalf("event retried too early: %+v, %v", due, err)
	}
	due, err := s.DueEvents(created.Meta.ID, retry, 10)
	if err != nil || len(due) != 1 || due[0].Attempts != 1 {
		t.Fatalf("retry = %+v, %v", due, err)
	}
	if err := s.NackEvent(id, retry, "permanent", true); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PendingEvents(created.Meta.ID); err != nil || n != 0 {
		t.Fatalf("dead event counted as pending: %d, %v", n, err)
	}
	queued, err := s.QueuedEvents(created.Meta.ID, 10)
	if err != nil || len(queued) != 1 || queued[0].State != "dead" || queued[0].Attempts != 2 {
		t.Fatalf("queued = %+v, %v; want the dead event", queued, err)
	}
}

func TestServiceStateAndNextWake(t *testing.T) {
	s := openSQLiteStore(t)
	if _, err := s.Create(Meta{Name: "svc", Role: "devops", Model: "test:model", Mode: "service"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetServiceSpec("svc", `{"goal":"watch the river"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetServiceState("svc", "suspended", "level 3.2 m"); err != nil {
		t.Fatal(err)
	}
	wake := time.Now().Truncate(time.Second).Add(5 * time.Minute)
	if err := s.SetNextWake("svc", &wake); err != nil {
		t.Fatal(err)
	}

	info, err := s.ServiceInfo("svc")
	if err != nil {
		t.Fatal(err)
	}
	if info.Spec != `{"goal":"watch the river"}` || info.State != "suspended" || info.Status != "level 3.2 m" {
		t.Fatalf("service info = %+v", info)
	}
	if info.NextWake == nil || !info.NextWake.Equal(wake) {
		t.Fatalf("next wake = %v, want %v", info.NextWake, wake)
	}
	meta, err := s.Get("svc")
	if err != nil || meta.Meta.NextWake == nil || !meta.Meta.NextWake.Equal(wake) {
		t.Fatalf("meta next wake = %+v, %v", meta, err)
	}

	if err := s.SetNextWake("svc", nil); err != nil {
		t.Fatal(err)
	}
	info, err = s.ServiceInfo("svc")
	if err != nil || info.NextWake != nil {
		t.Fatalf("cleared next wake = %v, %v", info.NextWake, err)
	}
}

func TestWatchersRoundTrip(t *testing.T) {
	s := openSQLiteStore(t)
	created, err := s.Create(Meta{Name: "svc", Role: "devops", Model: "test:model", Mode: "service"})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Truncate(time.Second)
	next := base.Add(time.Minute)
	if err := s.PutWatcher(Watcher{
		SessionID: created.Meta.ID, Name: "river", Spec: `{"kind":"command"}`,
		Enabled: true, NextRun: &next,
	}); err != nil {
		t.Fatal(err)
	}
	watchers, err := s.Watchers(created.Meta.ID)
	if err != nil || len(watchers) != 1 {
		t.Fatalf("watchers = %+v, %v", watchers, err)
	}
	w := watchers[0]
	if w.Name != "river" || !w.Enabled || w.Version != 1 || w.NextRun == nil || !w.NextRun.Equal(next) {
		t.Fatalf("stored watcher = %+v", w)
	}

	next = next.Add(time.Minute)
	if err := s.PutWatcher(Watcher{
		SessionID: created.Meta.ID, Name: "river", Spec: `{"kind":"timer"}`,
		Enabled: false, NextRun: &next, LastState: "3.2",
	}); err != nil {
		t.Fatal(err)
	}
	watchers, err = s.Watchers(created.Meta.ID)
	if err != nil || len(watchers) != 1 {
		t.Fatalf("upsert duplicated the watcher: %+v, %v", watchers, err)
	}
	w = watchers[0]
	if w.Spec != `{"kind":"timer"}` || w.Enabled || w.Version != 2 || w.LastState != "3.2" {
		t.Fatalf("updated watcher = %+v", w)
	}

	last := base
	if err := s.SetWatcherState(w.ID, &last, &next, "3.3", "probe failed"); err != nil {
		t.Fatal(err)
	}
	watchers, _ = s.Watchers(created.Meta.ID)
	if watchers[0].LastState != "3.3" || watchers[0].LastError != "probe failed" || watchers[0].LastRun == nil {
		t.Fatalf("state update = %+v", watchers[0])
	}
	if err := s.DeleteWatcher(w.ID); err != nil {
		t.Fatal(err)
	}
	if watchers, _ = s.Watchers(created.Meta.ID); len(watchers) != 0 {
		t.Fatalf("watcher was not deleted: %+v", watchers)
	}
}

func TestAppendEventRequiresSessionID(t *testing.T) {
	s := openSQLiteStore(t)
	if _, err := s.AppendEvent(core.Event{Class: core.EventUser, Kind: "test", Source: "user"}); err == nil {
		t.Fatal("an event without a session id was accepted")
	}
}
