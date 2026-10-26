package runner

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

type serviceTestProvider struct {
	mu      sync.Mutex
	replies []string
	calls   int
	done    chan int
}

func (p *serviceTestProvider) Completion(context.Context, provider.CompletionParams) (*provider.ChatCompletion, error) {
	p.mu.Lock()
	if p.calls >= len(p.replies) {
		p.mu.Unlock()
		return nil, fmt.Errorf("unexpected model call %d", p.calls+1)
	}
	reply := p.replies[p.calls]
	p.calls++
	p.mu.Unlock()
	select {
	case p.done <- p.calls:
	default:
	}
	return &provider.ChatCompletion{Choices: []provider.Choice{{
		Message:      provider.Message{Role: provider.RoleAssistant, Content: reply},
		FinishReason: "stop",
	}}}, nil
}

func (p *serviceTestProvider) CompletionStream(context.Context, provider.CompletionParams) (<-chan provider.ChatCompletionChunk, <-chan error) {
	panic("streaming is disabled in this test")
}

func (p *serviceTestProvider) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type serviceTestBackend struct {
	meta     Meta
	provider provider.Provider

	mu       sync.Mutex
	events   []core.Event
	acked    []int64
	nacked   []int64
	nextWake *time.Time
}

func (b *serviceTestBackend) List() ([]Meta, error) { return []Meta{b.meta}, nil }

func (b *serviceTestBackend) History(string, int, int) (History, error) {
	return History{Meta: b.meta}, nil
}

func (b *serviceTestBackend) Prepare(name string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), error) {
	cfg, p, rec, history, cleanup, _, err := b.PrepareWorkspace(name, b.meta.Cwd)
	return cfg, p, rec, history, cleanup, err
}

func (b *serviceTestBackend) PrepareWorkspace(string, string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), Meta, error) {
	cfg := &config.UserConfig{
		DefaultRole:  "devops",
		DefaultModel: "test:model",
		Context:      config.ContextConfig{KeepTurns: 4, ExecLimit: 8000, HeadLines: 80, TailLines: 40},
	}
	return cfg, b.provider, nil, nil, nil, b.meta, nil
}

func (b *serviceTestBackend) AppendEvent(sessionID string, e core.Event) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e.ID = int64(len(b.events) + 1)
	e.Session = sessionID
	b.events = append(b.events, e)
	return e.ID, nil
}

func (b *serviceTestBackend) AckEvents(_ string, ids []int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.acked = append(b.acked, ids...)
	return nil
}

func (b *serviceTestBackend) NackEvent(_ string, id int64, _ error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nacked = append(b.nacked, id)
	return nil
}

func (b *serviceTestBackend) DueEvents(string, time.Time, int) ([]core.Event, error) {
	return nil, nil
}

func (b *serviceTestBackend) SetNextWake(_ string, at *time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextWake = at
	return nil
}

func (b *serviceTestBackend) ackedCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.acked)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A registered service starts a resident worker on its first scheduler wake,
// runs one activation, acks the event, and returns to suspended. A user Send
// becomes a durable event and wakes the same worker.
func TestManagerRunsServiceWakes(t *testing.T) {
	fp := &serviceTestProvider{
		replies: []string{
			`{"action":"done","payload":"first","reason":"done"}`,
			`{"action":"done","payload":"second","reason":"done"}`,
		},
		done: make(chan int, 8),
	}
	backend := &serviceTestBackend{
		meta:     Meta{Name: "svc", ID: "sid-1", Mode: ModeService, Cwd: t.TempDir(), CreatedAt: time.Now()},
		provider: fp,
	}
	m := New(context.Background(), backend, 2)
	defer m.Close()
	if err := m.StartServices(); err != nil {
		t.Fatal(err)
	}

	if !m.WakeService("svc", core.Wake{
		Reason: "user input",
		Events: []core.Event{{ID: 1, Class: core.EventUser, Kind: "user", Payload: "go"}},
	}) {
		t.Fatal("registered service rejected a wake")
	}
	select {
	case <-fp.done:
	case <-time.After(3 * time.Second):
		t.Fatal("the service never called the model")
	}
	waitFor(t, "the first event to be acked", func() bool { return backend.ackedCount() == 1 })
	waitSnapshot(t, m, "svc", func(s Snapshot) bool { return s.State == StateSuspended && s.Pending == nil })

	if err := m.Send("svc", "again"); err != nil {
		t.Fatalf("Send to a service: %v", err)
	}
	select {
	case <-fp.done:
	case <-time.After(3 * time.Second):
		t.Fatal("the second instruction never called the model")
	}
	waitFor(t, "the user event to be acked", func() bool { return backend.ackedCount() == 2 })
	waitSnapshot(t, m, "svc", func(s Snapshot) bool { return s.State == StateSuspended })

	backend.mu.Lock()
	appended := len(backend.events)
	backend.mu.Unlock()
	if appended != 1 {
		t.Fatalf("appended events = %d, want the one Send event", appended)
	}
	if fp.CallCount() != 2 {
		t.Fatalf("model calls = %d, want 2", fp.CallCount())
	}
}
