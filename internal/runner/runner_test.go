package runner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

type fakeBackend struct {
	mu               sync.Mutex
	starts, cleanups int
	question         bool
	turns            map[string][]core.Turn
}

func (b *fakeBackend) List() ([]Meta, error) { return []Meta{{Name: "one"}}, nil }
func (b *fakeBackend) History(name string, offset, limit int) (History, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	turns := b.turns[name]
	offset = min(max(0, offset), len(turns))
	end := min(len(turns), offset+max(0, limit))
	return History{Meta: Meta{Name: name}, Total: len(turns), Turns: append([]core.Turn(nil), turns[offset:end]...)}, nil
}
func (b *fakeBackend) Prepare(name string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), error) {
	b.mu.Lock()
	b.starts++
	if b.turns == nil {
		b.turns = make(map[string][]core.Turn)
	}
	b.mu.Unlock()
	return &config.UserConfig{DefaultRole: "devops", DefaultModel: "test:model"}, &fakeProvider{question: b.question}, fakeRecorder{b, name}, nil, func() { b.mu.Lock(); b.cleanups++; b.mu.Unlock() }, nil
}

type workspaceBackend struct {
	fakeBackend
	dir string
}

func (b *workspaceBackend) PrepareWorkspace(name, cwd string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), string, error) {
	cfg, p, rec, history, cleanup, err := b.Prepare(name)
	return cfg, p, rec, history, cleanup, b.dir, err
}
func (b *workspaceBackend) Create(name, cwd string) (Meta, error) {
	dir, err := ResolveWorkingDir(cwd)
	return Meta{Name: name, Cwd: dir}, err
}

func TestWorkspacePlumbing(t *testing.T) {
	dir := t.TempDir()
	b := &workspaceBackend{dir: dir}
	m := New(context.Background(), b, 1)
	defer m.Close()
	meta, err := m.Create("one", dir)
	if err != nil || meta.Cwd != dir {
		t.Fatalf("create: %+v %v", meta, err)
	}
	if b.starts != 0 {
		t.Fatal("creation started runtime")
	}
	if err := m.SendWithWorkspace("one", "task", dir); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" })
	m.mu.Lock()
	got := m.entries["one"].rt.WorkingDir
	m.mu.Unlock()
	if got != dir {
		t.Fatalf("runtime cwd: %q", got)
	}
	if err := m.SendWithWorkspace("one", "task", t.TempDir()); err == nil {
		t.Fatal("active mismatch accepted")
	}
	if err := m.Send("one", "task"); err != nil {
		t.Fatal(err)
	}
	m.Close()
	if _, err := m.Create("two", dir); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed create: %v", err)
	}
	legacy := New(context.Background(), &fakeBackend{}, 1)
	defer legacy.Close()
	if err := legacy.SendWithWorkspace("one", "task", dir); err == nil {
		t.Fatal("legacy ignored workspace")
	}
	if _, err := legacy.Create("one", dir); err == nil {
		t.Fatal("legacy creation accepted")
	}
}

type fakeRecorder struct {
	b    *fakeBackend
	name string
}

func (r fakeRecorder) AppendTurn(t core.Turn) error {
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	r.b.turns[r.name] = append(r.b.turns[r.name], t)
	return nil
}

type fakeProvider struct {
	question bool
	calls    int
}

func (p *fakeProvider) Completion(ctx context.Context, _ provider.CompletionParams) (*provider.ChatCompletion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.calls++
	text := `{"action":"done","payload":"finished","reason":"ok"}`
	if p.question && p.calls == 1 {
		text = `{"action":"ask","payload":"Which target?","reason":"clarify"}`
	}
	return &provider.ChatCompletion{Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: text}}}}, nil
}
func (*fakeProvider) CompletionStream(context.Context, provider.CompletionParams) (<-chan provider.ChatCompletionChunk, <-chan error) {
	panic("unexpected stream")
}
func TestReasoningRecoveryBoundedAfterOverflow(t *testing.T) {
	m := New(context.Background(), &fakeBackend{}, 1)
	defer m.Close()
	w := &worker{m: m, name: "one", state: "busy", cancel: func() {}, subs: make(map[chan Event]struct{})}
	m.entries["one"] = w
	events, unsubscribe, err := m.Subscribe("one")
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	for i := 0; i < eventLimit+2; i++ {
		w.Reasoning("thought ")
	}
	for range events {
	}
	if len(w.subs) != 0 {
		t.Fatal("slow subscriber not disconnected")
	}
	w.Reasoning(strings.Repeat("界", reasoningLimit))
	recovery, stop, err := m.Subscribe("one")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	e := <-recovery
	if len(e.Snapshot.Reasoning) > reasoningLimit || len(e.Snapshot.Reasoning)%3 != 0 {
		t.Fatalf("unbounded or split reasoning tail: %d bytes", len(e.Snapshot.Reasoning))
	}
	w.ToolCall(core.ToolCall{Name: "read"})
	if s, _ := m.Snapshot("one"); s.Reasoning != "" {
		t.Fatal("stale reasoning after tool call")
	}
}

func waitSnapshot(t *testing.T, m *Manager, name string, match func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, err := m.Snapshot(name)
		if err == nil && match(s) {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", name)
	return Snapshot{}
}
func TestDuplicateStartsAndIdleEviction(t *testing.T) {
	b := &fakeBackend{}
	m := New(context.Background(), b, 1)
	defer m.Close()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Send("one", "task"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" && s.Queued == 0 })
	b.mu.Lock()
	starts := b.starts
	b.mu.Unlock()
	if starts != 1 {
		t.Fatalf("starts=%d", starts)
	}
	if err := m.Send("one", "reuse"); err != nil {
		t.Fatal(err)
	}
	// Wait for the queued instruction to be recorded, not just dequeued.
	deadline := time.Now().Add(5 * time.Second)
	for {
		h, _ := m.History("one", 0, 100)
		if h.Total >= 34 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued tasks did not complete")
		}
		time.Sleep(time.Millisecond)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" && s.Queued == 0 })
	if err := m.Send("two", "task"); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "two", func(s Snapshot) bool { return s.State == "awaiting" })
	if _, err := m.Snapshot("one"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	m.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.starts != 2 || b.cleanups != 2 {
		t.Fatalf("starts=%d cleanups=%d", b.starts, b.cleanups)
	}
}
func TestQuestionQueueReconnectAndCapacity(t *testing.T) {
	b := &fakeBackend{question: true}
	m := New(context.Background(), b, 1)
	defer m.Close()
	if err := m.Send("one", "task"); err != nil {
		t.Fatal(err)
	}
	s := waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.Pending != nil })
	if s.Pending.Kind != "ask" {
		t.Fatal(s)
	}
	if err := m.Send("two", "task"); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if err := m.Send("one", "next task"); err != nil {
		t.Fatal(err)
	}
	if err := m.Steer("one", "steering"); err != nil {
		t.Fatal(err)
	}
	ch, unsub, err := m.Subscribe("one")
	if err != nil {
		t.Fatal(err)
	}
	e := <-ch
	if e.Snapshot.Pending == nil || e.Snapshot.Pending.ID != s.Pending.ID {
		t.Fatal(e)
	}
	unsub()
	unsub()
	ch, unsub, err = m.Subscribe("one")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	if (<-ch).Snapshot.Pending == nil {
		t.Fatal("lost prompt")
	}
	if err := m.Reply("one", "stale", "answer", false); !errors.Is(err, ErrPrompt) {
		t.Fatal(err)
	}
	if err := m.Reply("one", s.Pending.ID, "target", false); err != nil {
		t.Fatal(err)
	}
	if err := m.Reply("one", s.Pending.ID, "duplicate", false); !errors.Is(err, ErrPrompt) {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" && s.Queued == 0 })
	h, _ := m.History("one", 0, 100)
	found := false
	for _, turn := range h.Turns {
		if turn.Kind == core.KindUserAnswer {
			found = true
			if turn.Content != "[user answer]\ntarget" {
				t.Fatalf("queue answered question: %+v", turn)
			}
		}
	}
	if !found {
		t.Fatal("missing answer")
	}
	if err := m.Cancel("one"); err != nil {
		t.Fatal(err)
	}
	s, _ = m.Snapshot("one")
	if s.State != "awaiting" {
		t.Fatal(s)
	}
}
func TestCancelPromptAndShutdownRaces(t *testing.T) {
	for i := 0; i < 10; i++ {
		m := New(context.Background(), &fakeBackend{question: true}, 1)
		if err := m.Send("one", "task"); err != nil {
			t.Fatal(err)
		}
		s := waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.Pending != nil })
		if err := m.Cancel("one"); err != nil {
			t.Fatal(err)
		}
		if err := m.Reply("one", s.Pending.ID, "late", true); !errors.Is(err, ErrPrompt) {
			t.Fatal(err)
		}
		waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" })
		var wg sync.WaitGroup
		for j := 0; j < 8; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ch, u, err := m.Subscribe("one")
				if err == nil {
					u()
					for range ch {
					}
				}
				_ = m.Send("one", "task")
				m.Close()
			}()
		}
		wg.Wait()
		if err := m.Send("one", "task"); !errors.Is(err, ErrClosed) {
			t.Fatal(err)
		}
	}
}
func TestConfirmAndSlowSubscriber(t *testing.T) {
	m := New(context.Background(), &fakeBackend{}, 1)
	defer m.Close()
	if err := m.Send("one", "task"); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" })
	m.mu.Lock()
	w := m.entries["one"]
	m.mu.Unlock()
	done := make(chan bool, 1)
	go func() { ok, err := w.Confirm("approve?"); done <- ok && err == nil }()
	s := waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.Pending != nil })
	if s.Pending.Kind != "confirm" {
		t.Fatal(s)
	}
	if err := m.Reply("one", s.Pending.ID, "", true); err != nil {
		t.Fatal(err)
	}
	if !<-done {
		t.Fatal("approval not delivered")
	}
	ch, u, err := m.Subscribe("one")
	if err != nil {
		t.Fatal(err)
	}
	defer u()
	for i := 0; i < eventLimit+1; i++ {
		w.Notice("event")
	}
	count := 0
	for range ch {
		count++
	}
	if count != eventLimit {
		t.Fatalf("buffer=%d", count)
	}
	for _, text := range []string{"/new x", "/rename x", "/NEW x", "/ rename x"} {
		if m.Send("one", text) == nil {
			t.Fatalf("accepted %q", text)
		}
	}
}
func TestApprovalReconnectToolCopy(t *testing.T) {
	m := New(context.Background(), &fakeBackend{}, 1)
	defer m.Close()
	ctx, cancel := context.WithCancel(m.ctx)
	w := &worker{m: m, name: "one", ctx: ctx, cancel: cancel, state: "busy", subs: make(map[chan Event]struct{})}
	m.mu.Lock()
	m.entries[w.name] = w
	m.mu.Unlock()
	tool := core.ToolCall{Name: "edit", Target: "config.go", Detail: "replace old with new", Reason: "fix config", Diff: "--- config.go\n+++ config.go\n-old\n+new\n"}
	w.ToolCall(tool)
	first, disconnect, err := m.Subscribe("one")
	if err != nil {
		t.Fatal(err)
	}
	initial := <-first
	if initial.Snapshot.Phase != "tool" || initial.Snapshot.ActiveTool == nil || *initial.Snapshot.ActiveTool != tool {
		t.Fatal(initial)
	}
	done := make(chan error, 1)
	go func() { _, err := w.Confirm("Apply edit?"); done <- err }()
	s := waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.Pending != nil })
	if s.Phase != "waiting_approval" || s.ActiveTool == nil || *s.ActiveTool != tool {
		t.Fatal(s)
	}
	if s.Pending.Tool == nil || *s.Pending.Tool != tool {
		t.Fatalf("missing approval details: %+v", s.Pending)
	}
	promptEvent := <-first
	p := promptEvent.Data.(Prompt)
	if p.Tool == nil || *p.Tool != tool {
		t.Fatalf("prompt data: %+v", p)
	}
	// Mutate all publicly exposed copies before reconnecting.
	s.Pending.Tool.Detail = "snapshot mutation"
	p.Tool.Diff = "data mutation"
	promptEvent.Snapshot.Pending.Tool.Detail = "event mutation"
	disconnect()
	reconnected, unsubscribe, err := m.Subscribe("one")
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	e := <-reconnected
	if e.Snapshot.Pending.Tool == nil || *e.Snapshot.Pending.Tool != tool {
		t.Fatalf("reconnect lost exact detail/diff: %+v", e.Snapshot.Pending)
	}
	e.Snapshot.Pending.Tool.Diff = "reconnect mutation"
	fresh, _ := m.Snapshot("one")
	if *fresh.Pending.Tool != tool {
		t.Fatal("snapshot aliases reconnect event")
	}
	if err := m.Reply("one", fresh.Pending.ID, "", true); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	resumed, _ := m.Snapshot("one")
	if resumed.Phase != "tool" || resumed.ActiveTool == nil || *resumed.ActiveTool != tool {
		t.Fatal(resumed)
	}

	for _, boundary := range []string{"consumed", "result", "awaiting", "ask"} {
		if boundary != "consumed" {
			w.ToolCall(tool)
		}
		switch boundary {
		case "result":
			w.ToolResult(core.ToolResult{OK: true})
		case "awaiting":
			w.Awaiting()
		case "ask":
			go func() { _, err := w.prompt("ask", "question?"); done <- err }()
			question := waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.Pending != nil })
			if question.Pending.Tool != nil {
				t.Fatal("question inherited tool")
			}
			if err := m.Reply("one", question.Pending.ID, "answer", false); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		go func() { _, err := w.Confirm("unrelated approval?"); done <- err }()
		pending := waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.Pending != nil })
		if pending.Pending.Tool != nil {
			t.Fatalf("%s retained stale tool: %+v", boundary, pending.Pending.Tool)
		}
		if err := m.Reply("one", pending.Pending.ID, "", false); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRuntimePhasesAndActiveTool(t *testing.T) {
	m := New(context.Background(), &fakeBackend{}, 1)
	defer m.Close()
	ctx, cancel := context.WithCancel(m.ctx)
	w := &worker{m: m, name: "one", ctx: ctx, cancel: cancel, state: "busy", phase: "waiting_model", subs: make(map[chan Event]struct{})}
	m.entries[w.name] = w
	check := func(phase string, tool *core.ToolCall) {
		t.Helper()
		s, err := m.Snapshot(w.name)
		if err != nil || s.Phase != phase || (s.ActiveTool == nil) != (tool == nil) {
			t.Fatalf("snapshot: %+v %v", s, err)
		}
		if tool != nil && *s.ActiveTool != *tool {
			t.Fatalf("tool: %+v", s.ActiveTool)
		}
	}
	check("waiting_model", nil)
	w.Reasoning("delta")
	check("reasoning", nil)
	w.Usage(core.Usage{})
	check("waiting_model", nil)
	w.Reasoning("delta")
	w.Reason("parsed action")
	check("waiting_model", nil)
	tool := core.ToolCall{Name: "remote", Target: "host", Detail: "echo hello"}
	w.ToolCall(tool)
	check("tool", &tool)
	ch, unsubscribe, err := m.Subscribe(w.name)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	e := <-ch
	if e.Snapshot.Phase != "tool" || *e.Snapshot.ActiveTool != tool {
		t.Fatal(e)
	}
	e.Snapshot.ActiveTool.Detail = "mutation"
	s, _ := m.Snapshot(w.name)
	s.ActiveTool.Target = "mutation"
	check("tool", &tool)
	w.ToolOutput("output")
	w.Usage(core.Usage{})
	check("tool", &tool)
	w.ToolResult(core.ToolResult{OK: true})
	check("waiting_model", nil)
	w.ToolCall(tool)
	w.Done("done")
	check("awaiting", nil)
	w.ToolCall(tool)
	w.Awaiting()
	check("awaiting", nil)
}

func TestActivitiesWithoutBrowser(t *testing.T) {
	m := New(context.Background(), &fakeBackend{}, 1)
	ch, unsubscribe, err := m.SubscribeActivities()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if err := m.Send("one", "task"); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" })
	m.Close()
	seen := map[string]bool{}
	for a := range ch {
		if a.Name != "one" || a.Event.Snapshot.Name != "one" {
			t.Fatalf("name: %+v", a)
		}
		seen[a.Event.Type] = true
	}
	for _, kind := range []string{"done", "awaiting", "stopped"} {
		if !seen[kind] {
			t.Fatalf("missing %s", kind)
		}
	}
	unsubscribe()
	if _, _, err := m.SubscribeActivities(); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestActivitiesBoundAndPayloadIsolation(t *testing.T) {
	m := New(context.Background(), &fakeBackend{}, 1)
	defer m.Close()
	ch, u, err := m.SubscribeActivities()
	if err != nil {
		t.Fatal(err)
	}
	defer u()
	other, stop, err := m.SubscribeActivities()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	w := &worker{m: m, name: "one", state: "busy"}
	tool := core.ToolCall{Name: "execute", Target: "sh", Detail: "echo hello"}
	w.ToolCall(tool)
	a := <-ch
	if a.Name != "one" || a.Event.Type != "tool_call" || a.Event.Data.(core.ToolCall) != tool {
		t.Fatalf("tool event: %+v", a)
	}
	<-other
	p := Prompt{Kind: "confirm", Tool: &tool}
	w.emit("prompt", p)
	a = <-ch
	a.Event.Data.(Prompt).Tool.Detail = "mutation"
	if got := (<-other).Event.Data.(Prompt).Tool.Detail; got != "echo hello" || tool.Detail != "echo hello" {
		t.Fatal("aliased payload")
	}
	for i := 0; i < 1000; i++ {
		w.Done("event")
	}
	if len(ch) != 128 {
		t.Fatalf("buffer=%d", len(ch))
	}
	u()
	u()
	count := 0
	for range ch {
		count++
	}
	if count != 128 {
		t.Fatalf("drained=%d", count)
	}
	stop()
	for i := 0; i < subscriberLimit; i++ {
		_, unsub, err := m.SubscribeActivities()
		if err != nil {
			t.Fatal(err)
		}
		defer unsub()
	}
	if _, _, err := m.SubscribeActivities(); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
}

func TestQueueBoundAndParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := New(ctx, &fakeBackend{question: true}, 1)
	defer m.Close()
	if err := m.Send("one", "task"); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.Pending != nil })
	for i := 0; i < queueLimit; i++ {
		if err := m.Send("one", "queued"); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Send("one", "overflow"); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	cancel()
	m.Close()
}
