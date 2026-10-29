package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/config"
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

	got, _, _, err := doStream(context.Background(), Ports{Provider: fp, Observer: obs}, provider.CompletionParams{}, false)
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

	_, _, _, err := doStream(context.Background(), Ports{Provider: fp, Observer: obs}, provider.CompletionParams{}, true)
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

	if _, _, _, err := doStream(context.Background(), Ports{Provider: fp, Observer: obs}, provider.CompletionParams{}, false); err == nil {
		t.Fatal("expected the provider error")
	}
	if fp.calls != 1 {
		t.Errorf("calls = %d, want 1", fp.calls)
	}
}

// finishProvider returns one chunk with a chosen finish_reason.
type finishProvider struct{ reason string }

func (p *finishProvider) Completion(context.Context, provider.CompletionParams) (*provider.ChatCompletion, error) {
	return &provider.ChatCompletion{Choices: []provider.Choice{{
		Message:      provider.Message{Content: `{}`},
		FinishReason: p.reason,
	}}}, nil
}

func (p *finishProvider) CompletionStream(context.Context, provider.CompletionParams) (<-chan provider.ChatCompletionChunk, <-chan error) {
	chunks := make(chan provider.ChatCompletionChunk, 1)
	errs := make(chan error, 1)
	chunks <- provider.ChatCompletionChunk{Choices: []provider.ChunkChoice{{
		Delta:        provider.ChunkDelta{Content: `{}`},
		FinishReason: p.reason,
	}}}
	close(chunks)
	errs <- nil
	return chunks, errs
}

// retryCompletionProvider records each request and returns a scripted reply.
type retryCompletionProvider struct {
	replies []string
	seen    [][]provider.Message
}

func (p *retryCompletionProvider) Completion(_ context.Context, params provider.CompletionParams) (*provider.ChatCompletion, error) {
	p.seen = append(p.seen, append([]provider.Message(nil), params.Messages...))
	reply := p.replies[min(len(p.seen)-1, len(p.replies)-1)]
	return &provider.ChatCompletion{Choices: []provider.Choice{{
		Message:      provider.Message{Content: reply},
		FinishReason: "stop",
	}}}, nil
}

func (p *retryCompletionProvider) CompletionStream(context.Context, provider.CompletionParams) (<-chan provider.ChatCompletionChunk, <-chan error) {
	panic("streaming is disabled in this test")
}

// A truncated assistant turn must not be replayed in full: it is large, it is
// unusable, and replaying it makes the retry slower and invites the model to
// continue the cut-off text. The placeholder keeps user/assistant alternation.
func TestTruncatedRetryOmitsLargeAssistantTurn(t *testing.T) {
	truncated := `{"action":"tool","toolname":"remote","payload":{"host":"h","cmd":"ls"},"reason":"r"`
	fp := &retryCompletionProvider{replies: []string{`{"action":"done","payload":"ok","reason":"r"}`}}
	obs := &recordingObserver{}
	cfg := &config.UserConfig{DefaultModel: "test:model", Streaming: false, Context: config.ContextConfig{KeepTurns: 4}}
	history := []provider.Message{
		{Role: provider.RoleUser, Content: "list the host"},
		{Role: provider.RoleAssistant, Content: truncated},
	}
	resp, _, err := ParseResponseWithRetry(context.Background(), cfg, &RolePrompt{}, Ports{Provider: fp, Observer: obs}, truncated, history)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Action != ActionDone {
		t.Fatalf("action = %q", resp.Action)
	}
	if len(fp.seen) != 1 {
		t.Fatalf("retry requests = %d, want 1", len(fp.seen))
	}
	var sawPlaceholder, sawTruncated bool
	for _, m := range fp.seen[0] {
		if m.Role == provider.RoleAssistant && m.Content == "[previous response was incomplete and omitted]" {
			sawPlaceholder = true
		}
		if strings.Contains(m.Content, `"host":"h"`) {
			sawTruncated = true
		}
	}
	if !sawPlaceholder {
		t.Fatal("retry request is missing the truncation placeholder")
	}
	if sawTruncated {
		t.Fatal("retry request still contains the full truncated response")
	}
	if len(obs.notices) == 0 || !strings.Contains(obs.notices[0], "incomplete") {
		t.Fatalf("notices = %v", obs.notices)
	}
}

// The finish reason must reach the caller intact; the parser uses it to name the
// real cause instead of guessing that every incomplete object hit a token limit.
func TestFinishReasonReachesTheParser(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			fp := &finishProvider{reason: "length"}
			cfg := &config.UserConfig{DefaultModel: "test:model", Streaming: stream}
			if stream {
				_, _, reason, err := doStream(context.Background(), Ports{Provider: fp, Observer: &recordingObserver{}}, provider.CompletionParams{}, false)
				if err != nil {
					t.Fatal(err)
				}
				if reason != "length" {
					t.Fatalf("finish reason = %q, want length", reason)
				}
				return
			}
			result, err := Chat(context.Background(), cfg, &RolePrompt{}, Ports{Provider: fp, Observer: &recordingObserver{}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result.FinishReason != "length" {
				t.Fatalf("finish reason = %q, want length", result.FinishReason)
			}
		})
	}
}

// The notice must distinguish a token limit from a dropped stream or a normal
// stop; the old wording blamed the output limit for every unbalanced object.
func TestIncompleteNoticeNamesTheCause(t *testing.T) {
	content := strings.Repeat("x", 32123) + `,"reason":"cut`
	limit := incompleteNotice(ReplyMeta{FinishReason: "length", Usage: &provider.Usage{CompletionTokens: 8000, ReasoningTokens: 1200}}, 1, content)
	for _, want := range []string{"output token limit", "8000 output tokens", "1200 reasoning", fmt.Sprintf("%d bytes", len(content)), `tail: "…`} {
		if !strings.Contains(limit, want) {
			t.Errorf("notice %q does not contain %q", limit, want)
		}
	}
	dropped := incompleteNotice(ReplyMeta{FinishReason: provider.FinishReasonIncomplete}, 2, "partial")
	if !strings.Contains(dropped, "stream ended without a terminal event") {
		t.Errorf("dropped-stream notice = %q", dropped)
	}
	normal := incompleteNotice(ReplyMeta{FinishReason: provider.FinishReasonStop}, 3, "partial")
	if !strings.Contains(normal, "normal stop") {
		t.Errorf("normal-stop notice = %q", normal)
	}
}
