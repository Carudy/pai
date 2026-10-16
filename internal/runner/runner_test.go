package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	rewinds          map[string]int
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

func (b *fakeBackend) Rewind(name string, keep int, backup *bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rewinds == nil {
		b.rewinds = make(map[string]int)
	}
	b.rewinds[name] = keep
	if turns := b.turns[name]; keep < len(turns) {
		b.turns[name] = turns[:keep]
	}
	return nil
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

type workingDirBackend struct {
	failureBackend
	dir string
}

func (b *workingDirBackend) SetWorkingDir(name, cwd string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if name != "one" {
		return ErrNotFound
	}
	if b.mutationErr != nil {
		return b.mutationErr
	}
	b.dir = cwd
	return nil
}

func (b *workingDirBackend) PrepareWorkspace(name, cwd string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), string, error) {
	b.mu.Lock()
	dir := b.dir
	b.mu.Unlock()
	dir, err := ResolveWorkingDir(dir)
	if err != nil {
		return nil, nil, nil, nil, nil, "", err
	}
	cfg, p, rec, history, cleanup, err := b.Prepare(name)
	return cfg, p, rec, history, cleanup, dir, err
}

func TestWorkingDirSwitch(t *testing.T) {
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	b := &workingDirBackend{dir: t.TempDir()}
	m := New(context.Background(), b, 1)
	defer m.Close()
	if err := m.Send("one", "task"); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" })
	history, err := m.History("one", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := m.SetWorkingDir("one", dir+string(os.PathSeparator)+"."); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	starts, cleanups, gotDir := b.starts, b.cleanups, b.dir
	b.mu.Unlock()
	if starts != 1 || cleanups != 1 || gotDir != dir {
		t.Fatalf("switch: starts=%d cleanups=%d dir=%q", starts, cleanups, gotDir)
	}
	if _, err := m.Snapshot("one"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retired runtime: %v", err)
	}
	gotHistory, err := m.History("one", 0, 100)
	if err != nil || !reflect.DeepEqual(gotHistory, history) {
		t.Fatalf("history changed: %+v %v", gotHistory, err)
	}
	if err := m.Send("one", "again"); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" })
	m.mu.Lock()
	runtimeDir := m.entries["one"].rt.WorkingDir
	m.mu.Unlock()
	b.mu.Lock()
	starts = b.starts
	b.mu.Unlock()
	if runtimeDir != dir || starts != 2 {
		t.Fatalf("rebuilt runtime: cwd=%q starts=%d", runtimeDir, starts)
	}
	if after, err := os.Getwd(); err != nil || after != before {
		t.Fatalf("process cwd changed: %q %v", after, err)
	}
}

func TestWorkingDirValidation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	b := &workingDirBackend{dir: dir}
	m := New(context.Background(), b, 1)
	defer m.Close()
	for _, cwd := range []string{"", " \t", filepath.Join(dir, "missing"), file} {
		if err := m.SetWorkingDir("one", cwd); err == nil {
			t.Fatalf("accepted invalid cwd %q", cwd)
		}
	}
	if err := m.SetWorkingDir("missing", dir); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session: %v", err)
	}
	relative, err := filepath.Rel(mustWorkingDir(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetWorkingDir("one", relative); err != nil {
		t.Fatal(err)
	}
	if b.dir != dir {
		t.Fatalf("relative cwd not normalized: %q", b.dir)
	}
	legacy := New(context.Background(), &fakeBackend{}, 1)
	defer legacy.Close()
	if err := legacy.SetWorkingDir("one", dir); err == nil {
		t.Fatal("unsupported backend accepted switch")
	}
	m.Close()
	if err := m.SetWorkingDir("one", dir); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed switch: %v", err)
	}
}

func mustWorkingDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestWorkingDirSwitchRejectsBusy(t *testing.T) {
	for _, state := range []string{"starting", "busy", "pending", "queued", "steering", "reserved"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			b := &workingDirBackend{dir: dir}
			m := New(context.Background(), b, 1)
			defer m.Close()
			w := &worker{state: "awaiting", tasks: make(chan string, 1), steering: make(chan string, 1)}
			switch state {
			case "starting", "busy":
				w.state = state
			case "pending":
				w.pending = &Prompt{ID: "pending"}
			case "queued":
				w.tasks <- "task"
			case "steering":
				w.steering <- "task"
			case "reserved":
				m.changing = map[string]bool{"one": true}
			}
			m.entries["one"] = w
			err := m.SetWorkingDir("one", t.TempDir())
			delete(m.entries, "one")
			if !errors.Is(err, ErrBusy) || b.dir != dir {
				t.Fatalf("busy switch: %v dir=%q", err, b.dir)
			}
		})
	}
}

func TestWorkingDirSwitchReservation(t *testing.T) {
	b := &workingDirBackend{dir: t.TempDir()}
	m := New(context.Background(), b, 1)
	defer m.Close()
	wctx, cancel := context.WithCancel(m.ctx)
	w := &worker{m: m, name: "one", ctx: wctx, cancel: cancel, state: "awaiting", done: make(chan struct{}), tasks: make(chan string, 1), steering: make(chan string, 1)}
	m.entries["one"] = w
	dir := t.TempDir()
	result := make(chan error, 1)
	go func() { result <- m.SetWorkingDir("one", dir) }()
	<-wctx.Done()
	// Keep the retirement join open while testing all competing mutations.
	release := sync.OnceFunc(func() {
		m.mu.Lock()
		delete(m.entries, "one")
		close(w.done)
		m.mu.Unlock()
	})
	defer release()
	if err := m.Send("one", "task"); !errors.Is(err, ErrBusy) {
		t.Fatalf("send during retirement: %v", err)
	}
	if err := m.SetWorkingDir("one", dir); !errors.Is(err, ErrBusy) {
		t.Fatalf("switch during retirement: %v", err)
	}
	if err := m.SetModel("one", "test:next"); !errors.Is(err, ErrBusy) {
		t.Fatalf("model switch during retirement: %v", err)
	}
	release()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := m.Send("one", "again"); err != nil {
		t.Fatalf("reservation not released: %v", err)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" })
}

func TestWorkingDirSwitchClearsFailure(t *testing.T) {
	b := &workingDirBackend{dir: filepath.Join(t.TempDir(), "missing")}
	m := New(context.Background(), b, 1)
	defer m.Close()
	s := sendAndWaitFailure(t, m, "one")
	dir := t.TempDir()
	b.mutationErr = errors.New("persist workspace failed")
	if err := m.SetWorkingDir("one", dir); !errors.Is(err, b.mutationErr) {
		t.Fatalf("failed persistence: %v", err)
	}
	if got, err := m.Snapshot("one"); err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("failed switch lost failure: %+v %v", got, err)
	}
	if err := m.SetWorkingDir("one", " "); err == nil {
		t.Fatal("blank workspace accepted")
	}
	if got, err := m.Snapshot("one"); err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("invalid switch lost failure: %+v %v", got, err)
	}
	b.mutationErr = nil
	if err := m.SetWorkingDir("one", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshot("one"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("successful switch retained failure: %v", err)
	}
	if err := m.Send("one", "retry"); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" && s.Error == "" })
	m.mu.Lock()
	got := m.entries["one"].rt.WorkingDir
	m.mu.Unlock()
	if got != dir {
		t.Fatalf("retry workspace: %q", got)
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
		// Pause on a confirmation prompt (the ask action no longer blocks).
		text = `{"action":"tool","toolname":"execute","payload":"printf not-trusted","reason":"needs approval"}`
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
	if s.Pending.Kind != "confirm" {
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
	if err := m.Reply("one", "stale", "answer", core.TrustDeny); !errors.Is(err, ErrPrompt) {
		t.Fatal(err)
	}
	if err := m.Reply("one", s.Pending.ID, "target", core.TrustDeny); err != nil {
		t.Fatal(err)
	}
	if err := m.Reply("one", s.Pending.ID, "duplicate", core.TrustDeny); !errors.Is(err, ErrPrompt) {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" && s.Queued == 0 })
	h, _ := m.History("one", 0, 100)
	found := false
	for _, turn := range h.Turns {
		if turn.Kind == core.KindToolResult {
			found = true
		}
	}
	if !found {
		t.Fatal("missing tool result after the queued reply")
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
		if err := m.Reply("one", s.Pending.ID, "late", core.TrustOnce); !errors.Is(err, ErrPrompt) {
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

// A command confirmation carries the untrusted names and resolves to the chosen
// trust level, so a browser can offer trust-for-this-run / trust-always.
func TestConfirmCommandResolvesTrustChoice(t *testing.T) {
	m := New(context.Background(), &fakeBackend{}, 1)
	defer m.Close()
	if err := m.Send("one", "task"); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" })

	m.mu.Lock()
	w := m.entries["one"]
	m.mu.Unlock()

	done := make(chan core.TrustChoice, 1)
	go func() { choice, _ := w.ConfirmCommand("Execute this command?", []string{"sudo", "rm"}); done <- choice }()

	s := waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.Pending != nil })
	if s.Pending.Kind != "confirm" || len(s.Pending.Untrusted) != 2 || s.Pending.Untrusted[0] != "sudo" {
		t.Fatalf("pending = %+v", s.Pending)
	}
	if err := m.Reply("one", s.Pending.ID, "", core.TrustPersist); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got != core.TrustPersist {
		t.Fatalf("choice = %v, want TrustPersist", got)
	}
}

// Trust-all approves every confirmation without a prompt, and the same live worker
// stops auto-approving the moment the flag is turned off.
func TestConfirmHonorsTrustAll(t *testing.T) {
	m := New(context.Background(), &fakeBackend{}, 1)
	defer m.Close()
	if err := m.Send("one", "task"); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" })
	m.mu.Lock()
	w := m.entries["one"]
	m.mu.Unlock()

	if err := m.SetTrustAll("one", true); err != nil {
		t.Fatal(err)
	}
	if !m.TrustAll("one") {
		t.Fatal("trust-all not recorded")
	}
	if choice, err := w.ConfirmCommand("Execute this command?", []string{"sudo"}); err != nil || choice != core.TrustOnce {
		t.Fatalf("ConfirmCommand = (%v, %v), want (TrustOnce, nil)", choice, err)
	}
	if choice, err := w.ConfirmPath("Write file?", "/tmp"); err != nil || choice != core.TrustOnce {
		t.Fatalf("ConfirmPath = (%v, %v), want (TrustOnce, nil)", choice, err)
	}
	if ok, err := w.Confirm("approve?"); err != nil || !ok {
		t.Fatalf("Confirm = (%v, %v), want (true, nil)", ok, err)
	}
	if s, _ := m.Snapshot("one"); s.Pending != nil || !s.TrustAll {
		t.Fatalf("trust-all did not skip the prompt: %+v", s)
	}

	// Off again: the next confirmation blocks on a prompt as before.
	if err := m.SetTrustAll("one", false); err != nil {
		t.Fatal(err)
	}
	if m.TrustAll("one") {
		t.Fatal("trust-all not cleared")
	}
	done := make(chan core.TrustChoice, 1)
	go func() { choice, _ := w.ConfirmCommand("Execute this command?", []string{"sudo"}); done <- choice }()
	s := waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.Pending != nil })
	if err := m.Reply("one", s.Pending.ID, "", core.TrustDeny); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got != core.TrustDeny {
		t.Fatalf("choice = %v, want TrustDeny", got)
	}
}

// Turning trust-all on answers an approval that is already waiting, so the switch
// takes effect at once instead of leaving a stale card up.
func TestTrustAllResolvesPendingApproval(t *testing.T) {
	m := New(context.Background(), &fakeBackend{question: true}, 1)
	defer m.Close()
	if err := m.Send("one", "task"); err != nil {
		t.Fatal(err)
	}
	s := waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.Pending != nil })
	if s.Pending.Kind != "confirm" {
		t.Fatal(s)
	}
	if err := m.SetTrustAll("one", true); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" && s.Pending == nil })
}

// A session grant is per name: trust is name-based, so a whole command line or a
// path would not mean what it looks like.
func TestSetCommandTrustScopesByName(t *testing.T) {
	m := New(context.Background(), &fakeBackend{}, 1)
	defer m.Close()
	if err := m.SetCommandTrust("one", "sudo", true); err != nil {
		t.Fatalf("known session rejected: %v", err)
	}
	if got := m.CommandTrust("one"); len(got) != 1 || got[0] != "sudo" {
		t.Fatalf("CommandTrust = %v, want [sudo]", got)
	}
	if err := m.SetCommandTrust("one", "sudo", false); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if got := m.CommandTrust("one"); len(got) != 0 {
		t.Fatalf("revoked name still granted: %v", got)
	}
	for _, bad := range []string{"", "   ", "sudo rm -rf /", "/usr/bin/sudo", strings.Repeat("x", 65)} {
		if err := m.SetCommandTrust("one", bad, true); err == nil {
			t.Errorf("SetCommandTrust(%q) = nil, want an error", bad)
		}
	}
	if err := m.SetCommandTrust("ghost", "sudo", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown session = %v, want ErrNotFound", err)
	}
}

// What a confirmation answered is remembered by the manager, so a grant the user
// just made is not forgotten when the runtime that asked is retired.
func TestConfirmationRemembersGrantedNames(t *testing.T) {
	m := New(context.Background(), &fakeBackend{}, 1)
	defer m.Close()
	if err := m.Send("one", "task"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	w := m.entries["one"]
	m.mu.Unlock()
	if w == nil {
		t.Fatal("worker not started")
	}

	done := make(chan core.TrustChoice, 1)
	go func() { choice, _ := w.ConfirmCommand("Execute this command?", []string{"sudo", "rm"}); done <- choice }()
	s := waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.Pending != nil })
	if err := m.Reply("one", s.Pending.ID, "", core.TrustSession); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got != core.TrustSession {
		t.Fatalf("choice = %v", got)
	}
	if got := m.CommandTrust("one"); len(got) != 2 || got[0] != "rm" || got[1] != "sudo" {
		t.Fatalf("granted names = %v, want [rm sudo]", got)
	}
}

func TestSetTrustAllValidatesName(t *testing.T) {
	m := New(context.Background(), &fakeBackend{}, 1)
	defer m.Close()
	if err := m.SetTrustAll("one", true); err != nil {
		t.Fatalf("known session rejected: %v", err)
	}
	if err := m.SetTrustAll("ghost", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown session accepted: %v", err)
	}
	if err := m.SetTrustAll("  ", true); err == nil {
		t.Fatal("blank name accepted")
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
	if err := m.Reply("one", s.Pending.ID, "", core.TrustOnce); err != nil {
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

func TestRewindTruncatesAndRetires(t *testing.T) {
	b := &fakeBackend{}
	m := New(context.Background(), b, 1)
	defer m.Close()
	if err := m.Rewind("one", 2, nil); err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if b.rewinds["one"] != 2 {
		t.Fatalf("backend keep = %d, want 2", b.rewinds["one"])
	}
	if err := m.Rewind("one", -1, nil); err == nil {
		t.Error("negative keep should be rejected")
	}
}

func TestRewindBusyRejected(t *testing.T) {
	b := &fakeBackend{}
	m := New(context.Background(), b, 1)
	defer m.Close()
	ctx, cancel := context.WithCancel(m.ctx)
	w := &worker{m: m, name: "one", ctx: ctx, cancel: cancel, state: "busy"}
	m.mu.Lock()
	m.entries["one"] = w
	m.mu.Unlock()
	if err := m.Rewind("one", 0, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("Rewind on a busy session = %v, want ErrBusy", err)
	}
	if _, called := b.rewinds["one"]; called {
		t.Error("backend reached while busy")
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
	if initial.Snapshot.Phase != "tool" || initial.Snapshot.ActiveTool == nil || !reflect.DeepEqual(*initial.Snapshot.ActiveTool, tool) {
		t.Fatal(initial)
	}
	done := make(chan error, 1)
	go func() { _, err := w.Confirm("Apply edit?"); done <- err }()
	s := waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.Pending != nil })
	if s.Phase != "waiting_approval" || s.ActiveTool == nil || !reflect.DeepEqual(*s.ActiveTool, tool) {
		t.Fatal(s)
	}
	if s.Pending.Tool == nil || !reflect.DeepEqual(*s.Pending.Tool, tool) {
		t.Fatalf("missing approval details: %+v", s.Pending)
	}
	promptEvent := <-first
	p := promptEvent.Data.(Prompt)
	if p.Tool == nil || !reflect.DeepEqual(*p.Tool, tool) {
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
	if e.Snapshot.Pending.Tool == nil || !reflect.DeepEqual(*e.Snapshot.Pending.Tool, tool) {
		t.Fatalf("reconnect lost exact detail/diff: %+v", e.Snapshot.Pending)
	}
	e.Snapshot.Pending.Tool.Diff = "reconnect mutation"
	fresh, _ := m.Snapshot("one")
	if !reflect.DeepEqual(*fresh.Pending.Tool, tool) {
		t.Fatal("snapshot aliases reconnect event")
	}
	if err := m.Reply("one", fresh.Pending.ID, "", core.TrustOnce); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	resumed, _ := m.Snapshot("one")
	if resumed.Phase != "tool" || resumed.ActiveTool == nil || !reflect.DeepEqual(*resumed.ActiveTool, tool) {
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
			go func() { _, err := w.prompt("ask", "question?", nil, ""); done <- err }()
			question := waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.Pending != nil })
			if question.Pending.Tool != nil {
				t.Fatal("question inherited tool")
			}
			if err := m.Reply("one", question.Pending.ID, "answer", core.TrustDeny); err != nil {
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
		if err := m.Reply("one", pending.Pending.ID, "", core.TrustDeny); err != nil {
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
		if tool != nil && !reflect.DeepEqual(*s.ActiveTool, *tool) {
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
	if e.Snapshot.Phase != "tool" || !reflect.DeepEqual(*e.Snapshot.ActiveTool, tool) {
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
	if a.Name != "one" || a.Event.Type != "tool_call" || !reflect.DeepEqual(a.Event.Data.(core.ToolCall), tool) {
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

type failureBackend struct {
	fakeBackend
	prepareErr     error
	prepareStarted chan struct{}
	prepareGate    chan struct{}
	mutationErr    error
}

func (b *failureBackend) Prepare(name string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), error) {
	b.mu.Lock()
	err, started, gate := b.prepareErr, b.prepareStarted, b.prepareGate
	b.mu.Unlock()
	if started != nil {
		close(started)
	}
	if gate != nil {
		<-gate
	}
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return b.fakeBackend.Prepare(name)
}

func (b *failureBackend) Models() ([]string, string, error) {
	return []string{"test:model"}, "test:model", nil
}
func (b *failureBackend) SetModel(string, string) error   { return b.mutationErr }
func (b *failureBackend) SetRole(string, string) error    { return b.mutationErr }
func (b *failureBackend) Rename(string, string) error     { return b.mutationErr }
func (b *failureBackend) Delete(string) error             { return b.mutationErr }
func (b *failureBackend) Rewind(string, int, *bool) error { return b.mutationErr }
func (b *failureBackend) Roles() ([]string, string, error) {
	return []string{"devops"}, "devops", nil
}
func (b *failureBackend) Create(name, cwd string) (Meta, error) {
	return Meta{Name: name, Cwd: cwd}, b.mutationErr
}
func (b *failureBackend) CreateWithRole(name, cwd, role string) (Meta, error) {
	return Meta{Name: name, Cwd: cwd, Role: role}, b.mutationErr
}

func sendAndWaitFailure(t *testing.T, m *Manager, name string) Snapshot {
	t.Helper()
	events, unsubscribe, err := m.SubscribeActivities()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if err := m.Send(name, "task"); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case a := <-events:
			if a.Name != name || a.Event.Type != "stopped" {
				continue
			}
			// Snapshot acquires the teardown lock, so worker removal is complete.
			s, err := m.Snapshot(name)
			if err != nil || s.State != "stopped" || s.Phase != "stopped" || s.Error == "" {
				t.Fatalf("failure snapshot: %+v %v", s, err)
			}
			m.mu.Lock()
			live := m.entries[name] != nil
			m.mu.Unlock()
			if live {
				t.Fatal("failed worker still occupies a runtime slot")
			}
			return s
		case <-timer.C:
			t.Fatal("timed out waiting for failed worker teardown")
		}
	}
}

func TestFailureSnapshotRecoveryAndRetry(t *testing.T) {
	failure := errors.New("stat working directory: no such file or directory")
	b := &failureBackend{prepareErr: failure}
	m := New(context.Background(), b, 1)
	defer m.Close()
	s := sendAndWaitFailure(t, m, "one")
	if s.Name != "one" || s.Error != failure.Error() || s.Queued != 0 || s.Pending != nil || s.ActiveTool != nil {
		t.Fatalf("stopped snapshot: %+v", s)
	}
	if _, _, err := m.Subscribe("one"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("subscribe to removed worker: %v", err)
	}
	if err := m.Cancel("one"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancel removed worker: %v", err)
	}
	if err := m.Send("one", " "); err == nil {
		t.Fatal("empty retry accepted")
	}
	if got, err := m.Snapshot("one"); err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("rejected retry changed failure: %+v %v", got, err)
	}
	m.mu.Lock()
	m.changing = map[string]bool{"one": true}
	m.mu.Unlock()
	if err := m.Send("one", "retry"); !errors.Is(err, ErrBusy) {
		t.Fatalf("reserved retry: %v", err)
	}
	if got, err := m.Snapshot("one"); err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("busy rejection changed failure: %+v %v", got, err)
	}
	m.mu.Lock()
	delete(m.changing, "one")
	m.mu.Unlock()

	started, gate := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	b.mu.Lock()
	b.prepareErr, b.prepareStarted, b.prepareGate = nil, started, gate
	b.mu.Unlock()
	if err := m.Send("one", "retry"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("retry did not start")
	}
	if got, err := m.Snapshot("one"); err != nil || got.State != "starting" || got.Error != "" {
		t.Fatalf("accepted retry snapshot: %+v %v", got, err)
	}
	m.mu.Lock()
	cached := len(m.failures)
	m.mu.Unlock()
	if cached != 0 {
		t.Fatal("accepted retry retained old failure")
	}
	release()
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" && s.Error == "" })
	m.Close()
	if _, err := m.Snapshot("one"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("successful retry resurrected old failure: %v", err)
	}
}

func TestFailureSnapshotRejectedCapacity(t *testing.T) {
	b := &failureBackend{prepareErr: errors.New("preparation failed")}
	m := New(context.Background(), b, 1)
	defer m.Close()
	s := sendAndWaitFailure(t, m, "one")
	b.mu.Lock()
	b.prepareErr = nil
	b.question = true
	b.mu.Unlock()
	if err := m.Send("blocker", "task"); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "blocker", func(s Snapshot) bool { return s.Pending != nil })
	if err := m.Send("one", "retry"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("retry at capacity: %v", err)
	}
	if got, err := m.Snapshot("one"); err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("capacity rejection changed failure: %+v %v", got, err)
	}
	m.Close()
	if err := m.Send("one", "retry"); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed retry: %v", err)
	}
	if got, err := m.Snapshot("one"); err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("closed rejection changed failure: %+v %v", got, err)
	}
}

func TestFailureSnapshotBoundedFIFO(t *testing.T) {
	b := &failureBackend{prepareErr: errors.New("preparation failed")}
	m := New(context.Background(), b, 1)
	defer m.Close()
	for i := 0; i < failureSnapshotLimit+2; i++ {
		sendAndWaitFailure(t, m, fmt.Sprintf("failed-%02d", i))
	}
	m.mu.Lock()
	cached, live := len(m.failures), len(m.entries)
	m.mu.Unlock()
	if cached != failureSnapshotLimit || live != 0 {
		t.Fatalf("cached=%d live=%d", cached, live)
	}
	for i := 0; i < failureSnapshotLimit+2; i++ {
		_, err := m.Snapshot(fmt.Sprintf("failed-%02d", i))
		if i < 2 && !errors.Is(err, ErrNotFound) || i >= 2 && err != nil {
			t.Fatalf("FIFO entry %d: %v", i, err)
		}
	}
	// Snapshot reads do not refresh FIFO order; a new failure for a name does.
	sendAndWaitFailure(t, m, "failed-02")
	if _, err := m.Snapshot("failed-03"); err != nil {
		t.Fatal(err)
	}
	sendAndWaitFailure(t, m, "another")
	if _, err := m.Snapshot("failed-03"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oldest failure was not evicted: %v", err)
	}
	if _, err := m.Snapshot("failed-02"); err != nil {
		t.Fatalf("latest failure for repeated name was evicted: %v", err)
	}
	b.mu.Lock()
	b.prepareErr = nil
	b.mu.Unlock()
	if err := m.Send("success", "task"); err != nil {
		t.Fatalf("failure cache consumed capacity: %v", err)
	}
	waitSnapshot(t, m, "success", func(s Snapshot) bool { return s.State == "awaiting" })
}

func TestFailureSnapshotMutationInvalidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Manager) error
	}{
		{"model", func(m *Manager) error { return m.SetModel("one", "test:next") }},
		{"role", func(m *Manager) error { return m.SetRole("one", "devops") }},
		{"rewind", func(m *Manager) error { return m.Rewind("one", 0, nil) }},
		{"rename", func(m *Manager) error { return m.Rename("one", "two") }},
		{"delete", func(m *Manager) error { return m.Delete("one") }},
		{"create", func(m *Manager) error { _, err := m.Create("one", ""); return err }},
		{"create-fallback", func(m *Manager) error { _, err := m.Create("one", ""); return err }},
		{"create-role", func(m *Manager) error { _, err := m.CreateWithRole("one", "", "devops"); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &failureBackend{prepareErr: errors.New("preparation failed")}
			var backend Backend = b
			if tc.name == "create-fallback" {
				backend = struct {
					Backend
					CreationBackend
				}{b, b}
			}
			m := New(context.Background(), backend, 1)
			defer m.Close()
			s := sendAndWaitFailure(t, m, "one")
			if tc.name == "rename" {
				sendAndWaitFailure(t, m, "two")
			}
			b.mutationErr = errors.New("mutation failed")
			if err := tc.mutate(m); !errors.Is(err, b.mutationErr) {
				t.Fatalf("rejected mutation: %v", err)
			}
			if got, err := m.Snapshot("one"); err != nil || !reflect.DeepEqual(got, s) {
				t.Fatalf("rejected mutation changed failure: %+v %v", got, err)
			}
			b.mutationErr = nil
			if err := tc.mutate(m); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Snapshot("one"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("successful mutation retained failure: %v", err)
			}
			if tc.name == "rename" {
				if _, err := m.Snapshot("two"); !errors.Is(err, ErrNotFound) {
					t.Fatalf("rename retained destination failure: %v", err)
				}
			}
		})
	}
}

func TestFailureSnapshotCopies(t *testing.T) {
	m := New(context.Background(), &fakeBackend{}, 1)
	defer m.Close()
	s := Snapshot{Name: "one", State: "stopped", Phase: "stopped", Error: "failed",
		ActiveTool: &core.ToolCall{CommandSegments: []string{"original"}, UntrustedSegments: []int{1}},
		Pending:    &Prompt{Untrusted: []string{"original"}, Tool: &core.ToolCall{CommandSegments: []string{"original"}}},
	}
	m.mu.Lock()
	m.rememberFailureLocked(s)
	m.mu.Unlock()
	s.ActiveTool.CommandSegments[0] = "changed"
	s.ActiveTool.UntrustedSegments[0] = 2
	s.Pending.Untrusted[0] = "changed"
	s.Pending.Tool.CommandSegments[0] = "changed"
	first, err := m.Snapshot("one")
	if err != nil {
		t.Fatal(err)
	}
	if first.ActiveTool.CommandSegments[0] != "original" || first.ActiveTool.UntrustedSegments[0] != 1 || first.Pending.Untrusted[0] != "original" || first.Pending.Tool.CommandSegments[0] != "original" {
		t.Fatalf("cache aliases source: %+v", first)
	}
	want := cloneSnapshot(first)
	first.ActiveTool.CommandSegments[0] = "changed"
	first.ActiveTool.UntrustedSegments[0] = 2
	first.Pending.Untrusted[0] = "changed"
	first.Pending.Tool.CommandSegments[0] = "changed"
	if second, err := m.Snapshot("one"); err != nil || !reflect.DeepEqual(second, want) {
		t.Fatalf("snapshot aliases cache: %+v %v", second, err)
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
