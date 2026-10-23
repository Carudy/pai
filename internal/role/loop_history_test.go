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

// scriptPrompter replays instructions, then ends the session.
type scriptPrompter struct {
	inputs []string
	next   int
}

func (p *scriptPrompter) Ask(string) (string, error) {
	if p.next >= len(p.inputs) {
		return "", core.ErrAborted
	}
	line := p.inputs[p.next]
	p.next++
	return line, nil
}

func (p *scriptPrompter) Confirm(string) (bool, error) { return true, nil }

// replayProvider records the messages each request carried.
type replayProvider struct {
	reply string
	calls [][]provider.Message
}

func (p *replayProvider) Completion(_ context.Context, params provider.CompletionParams) (*provider.ChatCompletion, error) {
	p.calls = append(p.calls, append([]provider.Message(nil), params.Messages...))
	return &provider.ChatCompletion{Choices: []provider.Choice{{
		Message: provider.Message{Role: provider.RoleAssistant, Content: p.reply},
	}}}, nil
}

func (p *replayProvider) CompletionStream(context.Context, provider.CompletionParams) (<-chan provider.ChatCompletionChunk, <-chan error) {
	panic("streaming is disabled in this test")
}

// A follow-up in the same interactive run must see the assistant's final reply.
// Dropping it makes "apply 1-5" meaningless: the numbered list the user is
// answering was the previous done payload.
func TestFollowUpSeesTheFinalReply(t *testing.T) {
	rp, err := chat.LoadRolePrompt("devops", "")
	if err != nil {
		t.Fatal(err)
	}
	fp := &replayProvider{reply: `{"action":"done","payload":"1. alpha\n2. beta","reason":"listed"}`}
	rt := &Runtime{
		Observer:    &fakeObserver{},
		Prompter:    &scriptPrompter{inputs: []string{"apply 1-5"}},
		Logger:      nopLogger{},
		Interactive: true,
		Provider:    fp,
	}
	cfg := &config.UserConfig{
		DefaultRole:  "devops",
		DefaultModel: "test:model",
		Context:      config.ContextConfig{KeepTurns: 4},
	}

	if err := loop(context.Background(), cfg, rt, rp, nil, "list the points"); err != nil {
		t.Fatal(err)
	}
	if len(fp.calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(fp.calls))
	}

	second := fp.calls[1]
	var sawReply, sawFollowUp bool
	for _, m := range second {
		if m.Role == provider.RoleAssistant && strings.Contains(m.Content, "1. alpha") {
			sawReply = true
		}
		if m.Role == provider.RoleUser && m.Content == "apply 1-5" {
			sawFollowUp = true
		}
	}
	if !sawFollowUp {
		t.Fatal("the follow-up instruction is missing from the second request")
	}
	if !sawReply {
		t.Errorf("the second request is missing the assistant's final reply:\n%s", transcript(second))
	}
}

// The same applies to a question: without the `ask` payload, the user's answer
// arrives as a non sequitur, and the model re-asks what was already settled.
func TestFollowUpSeesTheQuestionItAnswers(t *testing.T) {
	rp, err := chat.LoadRolePrompt("devops", "")
	if err != nil {
		t.Fatal(err)
	}
	fp := &replayProvider{reply: `{"action":"ask","payload":"Which dataset should I use?","reason":"need input"}`}
	rt := &Runtime{
		Observer:    &fakeObserver{},
		Prompter:    &scriptPrompter{inputs: []string{"the small one"}},
		Logger:      nopLogger{},
		Interactive: true,
		Provider:    fp,
	}
	cfg := &config.UserConfig{
		DefaultRole:  "devops",
		DefaultModel: "test:model",
		Context:      config.ContextConfig{KeepTurns: 4},
	}

	if err := loop(context.Background(), cfg, rt, rp, nil, "start the evaluation"); err != nil {
		t.Fatal(err)
	}
	if len(fp.calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(fp.calls))
	}
	second := fp.calls[1]
	var sawQuestion, sawAnswer bool
	for _, m := range second {
		if m.Role == provider.RoleAssistant && strings.Contains(m.Content, "Which dataset") {
			sawQuestion = true
		}
		if m.Role == provider.RoleUser && m.Content == "the small one" {
			sawAnswer = true
		}
	}
	if !sawAnswer {
		t.Fatal("the answer is missing from the second request")
	}
	if !sawQuestion {
		t.Errorf("the second request is missing the question being answered:\n%s", transcript(second))
	}
}

func transcript(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Role + "/" + m.Kind + ": " + m.Content + "\n")
	}
	return b.String()
}
