package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
	"github.com/Carudy/pai/internal/runner"
)

type streamingBackend struct {
	backend
	p *delayedProvider
}

func (b streamingBackend) Prepare(string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), error) {
	return &config.UserConfig{DefaultRole: "devops", Model: "fake", Streaming: true}, b.p, nil, nil, nil, nil
}

type delayedProvider struct {
	chunks  chan string
	finish  chan struct{}
	started chan struct{}
}

func (*delayedProvider) Completion(context.Context, provider.CompletionParams) (*provider.ChatCompletion, error) {
	panic("streaming disabled")
}
func (p *delayedProvider) CompletionStream(ctx context.Context, params provider.CompletionParams) (<-chan provider.ChatCompletionChunk, <-chan error) {
	if !params.Stream {
		panic("stream flag missing")
	}
	chunks := make(chan provider.ChatCompletionChunk)
	errs := make(chan error, 1)
	close(p.started)
	go func() {
		defer close(chunks)
		defer close(errs)
		for {
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			case s := <-p.chunks:
				select {
				case chunks <- provider.ChatCompletionChunk{Choices: []provider.ChunkChoice{{Delta: provider.ChunkDelta{Reasoning: &provider.Reasoning{Content: s}}}}}:
				case <-ctx.Done():
					errs <- ctx.Err()
					return
				}
			case <-p.finish:
				select {
				case chunks <- provider.ChatCompletionChunk{Choices: []provider.ChunkChoice{{Delta: provider.ChunkDelta{Content: `{"action":"done","payload":"ok"}`}}}}:
				case <-ctx.Done():
					errs <- ctx.Err()
					return
				}
				errs <- nil
				return
			}
		}
	}()
	return chunks, errs
}

func TestReasoningSSEBeforeCompletionAndRecovery(t *testing.T) {
	p := &delayedProvider{chunks: make(chan string), finish: make(chan struct{}), started: make(chan struct{})}
	m := runner.New(context.Background(), streamingBackend{p: p}, 1)
	defer m.Close()
	server := httptest.NewServer(New(m, Options{}))
	defer server.Close()
	if err := m.Send("one", "test"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider not started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/events?name=one", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("X-Accel-Buffering") != "no" || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers: %v", resp.Header)
	}
	events := make(chan runner.Event, 16)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
				var e runner.Event
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e) == nil {
					events <- e
				}
			}
		}
	}()
	for _, token := range []string{"first ", "second"} {
		select {
		case p.chunks <- token:
		case <-ctx.Done():
			t.Fatal("chunk blocked")
		}
		for {
			select {
			case e, ok := <-events:
				if !ok {
					t.Fatal("SSE closed before completion")
				}
				if e.Type != "reasoning" {
					continue
				}
				if e.Data != token || e.Snapshot.Phase != "reasoning" {
					t.Fatalf("reasoning event: %+v", e)
				}
			case <-ctx.Done():
				t.Fatal("reasoning buffered until completion")
			}
			break
		}
	}
	snapshot, err := m.Snapshot("one")
	if err != nil || snapshot.Reasoning != "first second" {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	recovery, unsub, err := m.Subscribe("one")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	if e := <-recovery; e.Snapshot.Reasoning != "first second" {
		t.Fatalf("recovery: %+v", e)
	}
	close(p.finish)
	for {
		select {
		case e := <-events:
			if e.Type == "done" {
				snapshot, _ = m.Snapshot("one")
				if snapshot.Reasoning != "" {
					t.Fatal("completed reasoning retained")
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("completion missing")
		}
	}
}
