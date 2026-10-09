package role

import (
	"testing"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/tool"
)

// pathPrompter implements core.PathConfirmer and always returns one choice.
type pathPrompter struct{ choice core.TrustChoice }

func (p pathPrompter) Ask(string) (string, error)   { return "", nil }
func (p pathPrompter) Confirm(string) (bool, error) { return false, nil }
func (p pathPrompter) ConfirmPath(string, string) (core.TrustChoice, error) {
	return p.choice, nil
}

func TestConfirmPathUsesCapability(t *testing.T) {
	rt := &Runtime{Observer: &fakeObserver{}, Prompter: pathPrompter{choice: core.TrustSession}}
	if got, err := confirmPath(rt, "Apply?", "/dir"); err != nil || got != core.TrustSession {
		t.Fatalf("got (%v, %v), want TrustSession", got, err)
	}
}

func TestApplyPathTrustSessionAndAlways(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rt := &Runtime{Observer: &fakeObserver{}, Logger: nopLogger{}}
	applyPathTrust(rt, core.TrustPersist, "/srv/project")

	if !tool.IsTrustedPath("/srv/project/a.txt", rt.trustedPaths(&config.UserConfig{})) {
		t.Fatal("session path trust missing")
	}
	cfg, err := config.LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range cfg.TrustedPaths {
		if p == "/srv/project" {
			return
		}
	}
	t.Fatalf("path not persisted: %v", cfg.TrustedPaths)
}

func TestApplyPathTrustRefusesRoot(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rt := &Runtime{Observer: &fakeObserver{}, Logger: nopLogger{}}
	applyPathTrust(rt, core.TrustPersist, "/")

	if !tool.IsTrustedPath("/etc/x", rt.trustedPaths(&config.UserConfig{})) {
		t.Fatal("root should still be trusted for this session")
	}
	cfg, _ := config.LoadUserConfig()
	for _, p := range cfg.TrustedPaths {
		if p == "/" {
			t.Fatal("the filesystem root must not be persisted")
		}
	}
}
