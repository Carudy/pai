package role

import (
	"context"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

type nopLogger struct{}

func (nopLogger) Debugf(string, ...any) {}
func (nopLogger) Errorf(string, ...any) {}

// steerOncePrompter implements core.Steerer: one steering message at the first
// poll, nothing after.
type steerOncePrompter struct {
	msg    string
	served bool
}

func (p *steerOncePrompter) Ask(string) (string, error)   { return "", core.ErrAborted }
func (p *steerOncePrompter) Confirm(string) (bool, error) { return false, nil }
func (p *steerOncePrompter) Steer() (string, bool) {
	if p.served {
		return "", false
	}
	p.served = true
	return p.msg, true
}

// recordingProvider captures each request's messages so a test can inspect what
// the model was actually sent.
type recordingProvider struct {
	seen    [][]provider.Message
	replies []string
}

func (p *recordingProvider) Completion(_ context.Context, params provider.CompletionParams) (*provider.ChatCompletion, error) {
	p.seen = append(p.seen, params.Messages)
	i := min(len(p.seen)-1, len(p.replies)-1)
	return &provider.ChatCompletion{Choices: []provider.Choice{
		{Message: provider.Message{Role: provider.RoleAssistant, Content: p.replies[i]}},
	}}, nil
}

func (p *recordingProvider) CompletionStream(context.Context, provider.CompletionParams) (<-chan provider.ChatCompletionChunk, <-chan error) {
	return nil, nil
}

// A message sent while the agent runs is injected at the next step boundary, so
// the model sees it before choosing its next action.
func TestSteeringIsInjectedBetweenSteps(t *testing.T) {
	rp, err := chat.LoadRolePrompt("devops", config.CustomPrompt{})
	if err != nil {
		t.Fatalf("load role: %v", err)
	}

	prov := &recordingProvider{replies: []string{
		`{"action":"tool","toolname":"execute","payload":"echo hi","reason":"r"}`,
		`{"action":"done","payload":"ok","reason":"r"}`,
	}}
	rt := &Runtime{
		Provider: prov,
		Observer: &fakeObserver{},
		Logger:   nopLogger{},
		Prompter: &steerOncePrompter{msg: "actually check the disk first"},
	}
	cfg := &config.UserConfig{DefaultRole: "devops", DefaultModel: "test:model"}

	if err := loop(context.Background(), cfg, rt, rp, nil, "run the task"); err != nil {
		t.Fatalf("loop: %v", err)
	}

	if len(prov.seen) < 2 {
		t.Fatalf("expected at least two model calls, got %d", len(prov.seen))
	}
	last := prov.seen[len(prov.seen)-1]
	for _, m := range last {
		if strings.Contains(m.Content, "[user steering]") && strings.Contains(m.Content, "check the disk") {
			return
		}
	}
	t.Errorf("steering message was not injected into the second request:\n%+v", last)
}
