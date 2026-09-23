package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

func TestShouldSummarize(t *testing.T) {
	c := config.ContextConfig{SummarizeAfterTokens: 100}

	if ShouldSummarize(c, 99) {
		t.Error("below the budget should not summarize")
	}
	if !ShouldSummarize(c, 100) {
		t.Error("at the budget should summarize")
	}
	if ShouldSummarize(config.ContextConfig{}, 1_000_000) {
		t.Error("a zero budget disables summarization")
	}
}

func TestSplitForSummary(t *testing.T) {
	msgs := make([]provider.Message, 5)
	for i := range msgs {
		msgs[i] = provider.Message{Role: provider.RoleUser, Content: string(rune('a' + i))}
	}

	span, recent := SplitForSummary(msgs, 2)
	if len(span) != 3 || len(recent) != 2 {
		t.Fatalf("got span=%d recent=%d, want 3/2", len(span), len(recent))
	}
	if span[0].Content != "a" || recent[0].Content != "d" {
		t.Errorf("split in the wrong place: span[0]=%q recent[0]=%q", span[0].Content, recent[0].Content)
	}

	// Nothing old enough to summarize.
	if span, _ := SplitForSummary(msgs, 5); span != nil {
		t.Errorf("expected an empty span, got %d", len(span))
	}
}

func TestEstimateTokens(t *testing.T) {
	msgs := []provider.Message{{Content: strings.Repeat("x", 400)}}
	if got := EstimateTokens(msgs); got != 100 {
		t.Errorf("EstimateTokens = %d, want 100", got)
	}
}

func TestSummaryMessage(t *testing.T) {
	m := SummaryMessage("  did the thing  ")
	if m.Role != provider.RoleSystem {
		t.Errorf("role = %q, want system", m.Role)
	}
	if m.Kind != core.KindSummary {
		t.Errorf("kind = %q, want summary", m.Kind)
	}
	if !strings.HasPrefix(m.Content, "[conversation summary so far]") {
		t.Errorf("missing header: %q", m.Content)
	}
	if !strings.Contains(m.Content, "did the thing") {
		t.Errorf("summary body missing: %q", m.Content)
	}
}

func TestRenderTranscriptLabels(t *testing.T) {
	got := RenderTranscript([]provider.Message{
		{Role: provider.RoleUser, Kind: core.KindInput, Content: "do it"},
		{Role: provider.RoleAssistant, Kind: core.KindOutput, Content: "ok"},
		{Role: provider.RoleUser, Kind: core.KindToolResult, Content: "out"},
	})
	for _, want := range []string{"USER: do it", "ASSISTANT: ok", "TOOL RESULT: out"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript missing %q:\n%s", want, got)
		}
	}
}

// fakeProvider captures the summarizer request so the test can assert it is a
// plain, non-streaming, non-reasoning call.
type fakeProvider struct {
	reply string
	got   provider.CompletionParams
	calls int
}

func (f *fakeProvider) Completion(_ context.Context, p provider.CompletionParams) (*provider.ChatCompletion, error) {
	f.calls++
	f.got = p
	return &provider.ChatCompletion{Choices: []provider.Choice{
		{Message: provider.Message{Role: provider.RoleAssistant, Content: f.reply}},
	}}, nil
}

func (f *fakeProvider) CompletionStream(context.Context, provider.CompletionParams) (<-chan provider.ChatCompletionChunk, <-chan error) {
	return nil, nil
}

func TestSummarizeUsesAPlainCall(t *testing.T) {
	fp := &fakeProvider{reply: "  a summary  "}
	ports := Ports{Provider: fp}

	got, err := Summarize(context.Background(), &config.UserConfig{Model: "m"}, ports, "USER: hi")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if got != "a summary" {
		t.Errorf("summary = %q, want trimmed", got)
	}
	if fp.got.ReasoningEffort != provider.ReasoningEffortNone {
		t.Errorf("reasoning = %q, want none", fp.got.ReasoningEffort)
	}
	if fp.got.Stream {
		t.Error("summarizer must not stream")
	}
	if fp.got.ResponseFormat != nil {
		t.Error("summarizer must not request JSON mode")
	}
	if len(fp.got.Messages) != 2 || fp.got.Messages[1].Content != "USER: hi" {
		t.Errorf("unexpected messages: %+v", fp.got.Messages)
	}
}

func TestSummarizeEmptyIsError(t *testing.T) {
	if _, err := Summarize(context.Background(), &config.UserConfig{}, Ports{Provider: &fakeProvider{reply: "  "}}, "x"); err == nil {
		t.Error("expected an error for an empty summary")
	}
}
