package role

import (
	"context"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
)

type recordingMailer struct {
	to, text string
	meta     core.MailMeta
}

func (r *recordingMailer) DeliverMail(to, text string, meta core.MailMeta) error {
	r.to, r.text, r.meta = to, text, meta
	return nil
}

func TestRunMailDelivers(t *testing.T) {
	obs := &fakeObserver{}
	mailer := &recordingMailer{}
	rt := &Runtime{Observer: obs, Mailer: mailer, Trace: "t1", Hops: 2}
	out, err := runMail(context.Background(), &config.UserConfig{}, rt, "handoff",
		[]byte(`{"to":"svc","text":"check the gate"}`))
	if err != nil {
		t.Fatalf("runMail: %v", err)
	}
	if mailer.to != "svc" || mailer.text != "check the gate" {
		t.Fatalf("delivered to %q text %q", mailer.to, mailer.text)
	}
	// The message continues the activation's chain: same trace, one hop later.
	if mailer.meta.Trace != "t1" || mailer.meta.Hops != 3 {
		t.Fatalf("meta = %+v, want trace t1 at hop 3", mailer.meta)
	}
	if len(obs.toolCalls) != 1 || obs.toolCalls[0].Name != "mail" || obs.toolCalls[0].Target != "svc" {
		t.Fatalf("tool call = %#v", obs.toolCalls)
	}
	if !strings.Contains(out, "check the gate") || !strings.Contains(out, "HOP: 3/6") {
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
