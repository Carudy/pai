package role

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
)

// fakeTasks is enough of the board to check the tool contract: payload decoding,
// the observation the model reads, and what happens without a board.
type fakeTasks struct {
	added   []core.Task
	claimed string
	updated core.Task
	status  string
	list    []core.Task
	err     error
}

func (f *fakeTasks) Add(title, detail string, priority int) (core.Task, error) {
	if f.err != nil {
		return core.Task{}, f.err
	}
	t := core.Task{ID: "t1", Title: title, Detail: detail, Priority: priority, Status: core.TaskOpen, Version: 1}
	f.added = append(f.added, t)
	return t, nil
}

func (f *fakeTasks) Claim(id string) (core.Task, error) {
	if f.err != nil {
		return core.Task{}, f.err
	}
	f.claimed = id
	return core.Task{ID: id, Title: "x", Status: core.TaskClaimed, Version: 2, OwnerName: "me"}, nil
}

func (f *fakeTasks) Update(id string, expectVersion int, status core.TaskStatus, note string) (core.Task, error) {
	if f.err != nil {
		return core.Task{}, f.err
	}
	f.updated = core.Task{ID: id, Status: status, Version: expectVersion + 1, Detail: note}
	f.status = string(status)
	return f.updated, nil
}

func (f *fakeTasks) List(status core.TaskStatus, limit int) ([]core.Task, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.list, nil
}

func taskRuntime(t *testing.T, tasks core.Tasks) *Runtime {
	t.Helper()
	return &Runtime{Observer: &fakeObserver{}, Tasks: tasks}
}

func TestTaskToolsAddClaimUpdateList(t *testing.T) {
	ft := &fakeTasks{}
	rt := taskRuntime(t, ft)
	cfg := &config.UserConfig{}

	out, err := runTaskAdd(context.Background(), cfg, rt, "plan", []byte(`{"title":"check the gate","detail":"after the dive","priority":70}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ft.added) != 1 || ft.added[0].Title != "check the gate" || ft.added[0].Priority != 70 {
		t.Fatalf("added = %+v", ft.added)
	}
	if !strings.Contains(out, "task added") || !strings.Contains(out, "t1") {
		t.Fatalf("add observation = %q", out)
	}

	out, err = runTaskClaim(context.Background(), cfg, rt, "start", []byte(`{"id":"t1"}`))
	if err != nil || ft.claimed != "t1" {
		t.Fatalf("claim = %q %v", out, err)
	}
	if !strings.Contains(out, "task claimed") || !strings.Contains(out, "owner=me") {
		t.Fatalf("claim observation = %q", out)
	}

	out, err = runTaskUpdate(context.Background(), cfg, rt, "done", []byte(`{"id":"t1","expect_version":2,"status":"done","note":"gate open"}`))
	if err != nil {
		t.Fatal(err)
	}
	if ft.status != "done" || ft.updated.ID != "t1" {
		t.Fatalf("update = %+v", ft.updated)
	}
	if !strings.Contains(out, "task updated") || !strings.Contains(out, "done") {
		t.Fatalf("update observation = %q", out)
	}

	ft.list = []core.Task{{ID: "t1", Title: "check the gate", Status: core.TaskOpen, Version: 1}}
	out, err = runTaskList(context.Background(), cfg, rt, "read", []byte(`{}`))
	if err != nil || !strings.Contains(out, "check the gate") {
		t.Fatalf("list = %q %v", out, err)
	}

	ft.list = nil
	if out, _ := runTaskList(context.Background(), cfg, rt, "read", nil); !strings.Contains(out, "none") {
		t.Fatalf("empty list = %q", out)
	}
}

func TestTaskToolsValidatePayloads(t *testing.T) {
	rt := taskRuntime(t, &fakeTasks{})
	cfg := &config.UserConfig{}
	for _, tc := range []struct{ name, payload string }{
		{"task_add", `{"detail":"no title"}`},
		{"task_claim", `{}`},
		{"task_update", `{"id":"t1","status":"done"}`},    // no version
		{"task_update", `{"id":"t1","expect_version":1}`}, // no status
		{"task_add", `not json`},
	} {
		handler, ok := toolHandlers[tc.name]
		if !ok {
			t.Fatalf("%s has no handler", tc.name)
		}
		if _, err := handler(context.Background(), cfg, rt, "", []byte(tc.payload)); err == nil {
			t.Errorf("%s accepted %s", tc.name, tc.payload)
		}
	}
}

// Without a board the tools must say so as an observation, not fail the run.
func TestTaskToolsWithoutABoard(t *testing.T) {
	rt := taskRuntime(t, nil)
	for name, payload := range map[string]string{
		"task_add":    `{"title":"x"}`,
		"task_claim":  `{"id":"t1"}`,
		"task_update": `{"id":"t1","expect_version":1,"status":"done"}`,
		"task_list":   `{}`,
	} {
		out, err := toolHandlers[name](context.Background(), &config.UserConfig{}, rt, "", []byte(payload))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(out, "no group board") {
			t.Errorf("%s observation = %q", name, out)
		}
	}
}

// A stale write is surfaced with the current version, which is the only thing
// the model needs to recover.
func TestTaskUpdateReportsStaleVersion(t *testing.T) {
	ft := &fakeTasks{err: errors.New("task was changed by someone else: current version is 4")}
	out, err := runTaskUpdate(context.Background(), &config.UserConfig{}, taskRuntime(t, ft), "done",
		[]byte(`{"id":"t1","expect_version":2,"status":"done"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "current version is 4") {
		t.Fatalf("observation = %q", out)
	}
}
