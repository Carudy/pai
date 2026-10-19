package role

import (
	"context"
	"testing"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

// usageProvider answers every request and reports usage, the way a real endpoint does.
type usageProvider struct{ reply string }

func (p usageProvider) Completion(context.Context, provider.CompletionParams) (*provider.ChatCompletion, error) {
	return &provider.ChatCompletion{
		Choices: []provider.Choice{{Message: provider.Message{Role: provider.RoleAssistant, Content: p.reply}}},
		Usage:   &provider.Usage{PromptTokens: 900, CompletionTokens: 10, TotalTokens: 910},
	}, nil
}

func (usageProvider) CompletionStream(context.Context, provider.CompletionParams) (<-chan provider.ChatCompletionChunk, <-chan error) {
	return nil, nil
}

// usageRecorder stands in for a session store that can also hold token accounting.
type usageRecorder struct {
	report, estimate int
	set              bool
}

func (r *usageRecorder) AppendTurn(core.Turn) error { return nil }

func (r *usageRecorder) SetUsage(reported, estimate int) error {
	r.report, r.estimate, r.set = reported, estimate, true
	return nil
}

// One step records what the provider counted next to PAI's estimate of the very prompt
// that was sent: that ratio is what calibrates every later estimate, and it is only
// useful if both halves describe the same request.
func TestStepRecordsTheTokenEstimatePair(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rp, err := chat.LoadRolePrompt("devops", "")
	if err != nil {
		t.Fatalf("load role: %v", err)
	}
	rec := &usageRecorder{}
	rt := &Runtime{
		Observer: &fakeObserver{},
		Logger:   nopLogger{},
		Provider: usageProvider{reply: `{"action":"done","payload":"ok","reason":"r"}`},
		Recorder: rec,
	}
	cfg := &config.UserConfig{DefaultRole: "devops", DefaultModel: "test:model"}
	history := []provider.Message{{Role: provider.RoleUser, Kind: core.KindInput, Content: "hi"}}

	next, _, err := step(context.Background(), cfg, rt, rp, history)
	if err != nil {
		t.Fatal(err)
	}
	if next {
		t.Fatal("a done action should end the step")
	}
	if !rec.set {
		t.Fatal("the step did not record the usage pair")
	}
	if rec.report != 900 {
		t.Errorf("reported = %d, want the provider's 900", rec.report)
	}
	// The estimate must be of the prompt that was sent: head, served history, tail.
	want := chat.EstimateTokens(rp.Messages(chat.ServedHistory(history, cfg.Context)))
	if rec.estimate != want {
		t.Errorf("estimate = %d, want %d for the composed prompt", rec.estimate, want)
	}
	if rt.promptTokens != 900 {
		t.Errorf("budget = %d, want the reported 900", rt.promptTokens)
	}
}
