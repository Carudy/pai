package role

import (
	"context"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/config"
)

type recordingMailer struct{ to, text string }

func (r *recordingMailer) DeliverMail(to, text string) error {
	r.to, r.text = to, text
	return nil
}

func TestRunMailDelivers(t *testing.T) {
	obs := &fakeObserver{}
	mailer := &recordingMailer{}
	rt := &Runtime{Observer: obs, Mailer: mailer}
	out, err := runMail(context.Background(), &config.UserConfig{}, rt, "handoff",
		[]byte(`{"to":"svc","text":"check the gate"}`))
	if err != nil {
		t.Fatalf("runMail: %v", err)
	}
	if mailer.to != "svc" || mailer.text != "check the gate" {
		t.Fatalf("delivered to %q text %q", mailer.to, mailer.text)
	}
	if len(obs.toolCalls) != 1 || obs.toolCalls[0].Name != "mail" || obs.toolCalls[0].Target != "svc" {
		t.Fatalf("tool call = %#v", obs.toolCalls)
	}
	if !strings.Contains(out, "check the gate") {
		t.Fatalf("observation = %q", out)
	}
}

func TestRunMailValidates(t *testing.T) {
	for _, payload := range []string{`{"to":"svc"}`, `{"text":"hi"}`, `{"to":" ","text":"hi"}`, `{`} {
		if _, err := runMail(context.Background(), &config.UserConfig{}, &Runtime{}, "", []byte(payload)); err == nil {
			t.Errorf("payload %s was accepted", payload)
		}
	}
	if out, err := runMail(context.Background(), &config.UserConfig{}, &Runtime{Observer: &fakeObserver{}}, "", []byte(`{"to":"svc","text":"hi"}`)); err != nil || !strings.Contains(out, "cannot deliver") {
		t.Fatalf("nil mailer: %q %v", out, err)
	}
}
