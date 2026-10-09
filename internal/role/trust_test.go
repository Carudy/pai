package role

import (
	"testing"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/tool"
)

// choicePrompter implements core.CommandConfirmer and always returns one choice.
type choicePrompter struct{ choice core.TrustChoice }

func (p choicePrompter) Ask(string) (string, error)   { return "", nil }
func (p choicePrompter) Confirm(string) (bool, error) { return false, nil }
func (p choicePrompter) ConfirmCommand(string, []string) (core.TrustChoice, error) {
	return p.choice, nil
}

func TestConfirmCommandUsesCapability(t *testing.T) {
	rt := &Runtime{Observer: &fakeObserver{}, Prompter: choicePrompter{choice: core.TrustPersist}}
	if got, err := confirmCommand(rt, "title", []string{"ls"}); err != nil || got != core.TrustPersist {
		t.Fatalf("got (%v, %v), want TrustPersist", got, err)
	}
}

func TestConfirmCommandFallsBackToYesNo(t *testing.T) {
	yes := &Runtime{Observer: &fakeObserver{}, Prompter: stubPrompter{ok: true}}
	if got, _ := confirmCommand(yes, "t", nil); got != core.TrustOnce {
		t.Errorf("yes -> %v, want TrustOnce", got)
	}
	no := &Runtime{Observer: &fakeObserver{}, Prompter: stubPrompter{ok: false}}
	if got, _ := confirmCommand(no, "t", nil); got != core.TrustDeny {
		t.Errorf("no -> %v, want TrustDeny", got)
	}
}

func TestApplyTrustSessionAddsToRuntime(t *testing.T) {
	rt := &Runtime{Observer: &fakeObserver{}, Logger: nopLogger{}}
	applyTrust(rt, core.TrustSession, []string{"ls", "git"})
	if !tool.IsTrusted("ls && git status", rt.trustedList(&config.UserConfig{})) {
		t.Fatalf("session trust not honored: %v", rt.TrustedCmds)
	}
}

func TestApplyTrustDowngradesInterpreter(t *testing.T) {
	obs := &fakeObserver{}
	rt := &Runtime{Observer: obs, Logger: nopLogger{}}
	applyTrust(rt, core.TrustPersist, []string{"sudo"})
	if !tool.IsTrusted("sudo x", rt.trustedList(&config.UserConfig{})) {
		t.Fatal("sudo should still be trusted for this session")
	}
	if obs.lastNotice() == "" {
		t.Error("expected a notice about the downgrade")
	}
}

func TestApplyTrustPersistsSafeName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rt := &Runtime{Observer: &fakeObserver{}, Logger: nopLogger{}}
	applyTrust(rt, core.TrustPersist, []string{"ls"})

	cfg, err := config.LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range cfg.TrustedCmds {
		if n == "ls" {
			return
		}
	}
	t.Fatalf("ls not persisted: %v", cfg.TrustedCmds)
}
