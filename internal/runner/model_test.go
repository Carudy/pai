package runner

import (
	"context"
	"errors"
	"testing"
)

type modelBackend struct {
	fakeBackend
	model   string
	entered chan struct{}
	release chan struct{}
}

func (b *modelBackend) Models() ([]string, string, error) {
	return []string{"test:model"}, "test:model", nil
}
func (b *modelBackend) SetModel(name, model string) error {
	if b.entered != nil {
		close(b.entered)
		<-b.release
	}
	b.model = model
	return nil
}

func TestModelSwitch(t *testing.T) {
	b := &modelBackend{}
	m := New(context.Background(), b, 1)
	defer m.Close()
	if err := m.Send("one", "task"); err != nil {
		t.Fatal(err)
	}
	s := waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" })
	_ = s
	if snapshot, err := m.Snapshot("one"); err != nil || snapshot.Model != "test:model" {
		t.Fatalf("model: %+v %v", snapshot, err)
	}
	if err := m.SetModel("one", "test:next"); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	cleanups := b.cleanups
	b.mu.Unlock()
	if cleanups != 1 || b.model != "test:next" {
		t.Fatal("runtime not joined before switch")
	}
	if _, err := m.Snapshot("one"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := m.Send("one", "again"); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.State == "awaiting" })
}

func TestModelSwitchRejectsBusy(t *testing.T) {
	b := &modelBackend{fakeBackend: fakeBackend{question: true}}
	m := New(context.Background(), b, 1)
	defer m.Close()
	if err := m.Send("one", "task"); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, m, "one", func(s Snapshot) bool { return s.Pending != nil })
	if err := m.SetModel("one", "test:next"); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if b.model != "" {
		t.Fatal("busy switch persisted")
	}
}

func TestModelSwitchReservation(t *testing.T) {
	b := &modelBackend{}
	m := New(context.Background(), b, 1)
	defer m.Close()
	// A blocked idle worker cleanup forces the join window to stay open.
	wctx, cancel := context.WithCancel(m.ctx)
	w := &worker{m: m, name: "one", ctx: wctx, cancel: cancel, state: "awaiting", done: make(chan struct{}), tasks: make(chan string, 1), steering: make(chan string, 1)}
	m.entries["one"] = w
	result := make(chan error, 1)
	go func() { result <- m.SetModel("one", "test:next") }()
	<-wctx.Done()
	if err := m.Send("one", "task"); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err := m.SetModel("one", "test:other"); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	m.mu.Lock()
	delete(m.entries, "one")
	close(w.done)
	m.mu.Unlock()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
