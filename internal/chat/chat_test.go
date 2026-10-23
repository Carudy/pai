package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

// retryProvider scripts a first streaming attempt (a stall, optionally after
// showing reasoning) and a successful retry.
type retryProvider struct {
	calls     int
	reasoning string
	reply     string
	firstErr  error
}

func (p *retryProvider) Completion(context.Context, provider.CompletionParams) (*provider.ChatCompletion, error) {
	return nil, errors.New("non-streaming path is not used here")
}

func (p *retryProvider) CompletionStream(context.Context, provider.CompletionParams) (<-chan provider.ChatCompletionChunk, <-chan error) {
	p.calls++
	chunks := make(chan provider.ChatCompletionChunk, 1)
	errs := make(chan error, 1)
	if p.calls == 1 {
		if p.reasoning != "" {
			chunks <- provider.ChatCompletionChunk{Choices: []provider.ChunkChoice{{
				Delta: provider.ChunkDelta{Reasoning: &provider.Reasoning{Content: p.reasoning}},
			}}}
		}
		close(chunks)
		if p.firstErr != nil {
			errs <- p.firstErr
		} else {
			errs <- fmt.Errorf("test: %w: no data for 90s", provider.ErrStalled)
		}
		return chunks, errs
	}
	chunks <- provider.ChatCompletionChunk{Choices: []provider.ChunkChoice{{
		Delta: provider.ChunkDelta{Content: p.reply},
	}}}
	close(chunks)
	errs <- nil
	return chunks, errs
}

// recordingObserver embeds the port so only the methods under test need bodies.
type recordingObserver struct {
	core.Observer
	notices   []string
	reasoning []string
}

func (o *recordingObserver) Notice(s string)    { o.notices = append(o.notices, s) }
func (o *recordingObserver) Reasoning(s string) { o.reasoning = append(o.reasoning, s) }

// A stall before anything reached the reader is indistinguishable from a request
// that never got going, so it is resent once — what the reader would do by hand.
func TestStreamRetriesAStallBeforeOutput(t *testing.T) {
	fp := &retryProvider{reply: "hello"}
	obs := &recordingObserver{}

	got, _, err := doStream(context.Background(), Ports{Provider: fp, Observer: obs}, provider.CompletionParams{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Errorf("content = %q, want the retried reply", got)
	}
	if fp.calls != 2 {
		t.Errorf("calls = %d, want 2", fp.calls)
	}
	if len(obs.notices) != 1 || !strings.Contains(obs.notices[0], "retrying once") {
		t.Errorf("notices = %v, want one retry notice", obs.notices)
	}
}

// Once reasoning has been streamed, a retry would duplicate it on screen: report
// the stall instead.
func TestStreamDoesNotRetryAfterReasoning(t *testing.T) {
	fp := &retryProvider{reasoning: "thinking", reply: "hello"}
	obs := &recordingObserver{}

	_, _, err := doStream(context.Background(), Ports{Provider: fp, Observer: obs}, provider.CompletionParams{}, true)
	if !errors.Is(err, provider.ErrStalled) {
		t.Fatalf("error = %v, want ErrStalled", err)
	}
	if fp.calls != 1 {
		t.Errorf("calls = %d, want 1 (no duplicate reasoning)", fp.calls)
	}
	if len(obs.reasoning) != 1 {
		t.Errorf("reasoning = %v, want the first attempt's text", obs.reasoning)
	}
	if len(obs.notices) != 0 {
		t.Errorf("notices = %v, want none", obs.notices)
	}
}

// Only a stall is retried; a malformed or rejected response is not.
func TestStreamDoesNotRetryOtherErrors(t *testing.T) {
	fp := &retryProvider{reply: "hello", firstErr: errors.New("bad response format")}
	obs := &recordingObserver{}

	if _, _, err := doStream(context.Background(), Ports{Provider: fp, Observer: obs}, provider.CompletionParams{}, false); err == nil {
		t.Fatal("expected the provider error")
	}
	if fp.calls != 1 {
		t.Errorf("calls = %d, want 1", fp.calls)
	}
}
