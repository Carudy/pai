package role

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
)

// recordingPrompter captures the untrusted names a command confirmation offers,
// and declines so nothing runs.
type recordingPrompter struct{ untrusted []string }

func (p *recordingPrompter) Ask(string) (string, error)   { return "", nil }
func (p *recordingPrompter) Confirm(string) (bool, error) { return false, nil }
func (p *recordingPrompter) ConfirmCommand(_ string, untrusted []string) (core.TrustChoice, error) {
	p.untrusted = untrusted
	return core.TrustDeny, nil
}

// A navigation builtin needs no grant, so `cd /tmp && ls` is not offered "trust
// cd" — only the untrusted `ls` is flagged.
func TestExecuteDoesNotOfferToTrustNavigationBuiltins(t *testing.T) {
	p := &recordingPrompter{}
	rt := &Runtime{Observer: &fakeObserver{}, Prompter: p, Logger: nopLogger{}}

	if _, err := runExecute(context.Background(), &config.UserConfig{}, rt, "list", json.RawMessage(`"cd /tmp && ls"`)); err != nil {
		t.Fatal(err)
	}
	if len(p.untrusted) != 1 || p.untrusted[0] != "ls" {
		t.Fatalf("offered untrusted names %q, want [ls]", p.untrusted)
	}
}

// The execute payload accepts a bare command or an object with a command and an
// optional directory.
func TestParseExecPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantCmd string
		wantDir string
		wantErr bool
	}{
		{"string form", `"df -h"`, "df -h", "", false},
		{"object form", `{"cmd":"df -h","dir":"/var/log"}`, "df -h", "/var/log", false},
		{"object without dir", `{"cmd":"ls"}`, "ls", "", false},
		{"object without cmd", `{"dir":"/tmp"}`, "", "/tmp", false},
		{"not json", `df -h`, "", "", true},
		{"empty", ``, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseExecPayload(json.RawMessage(tc.payload))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Cmd != tc.wantCmd || got.Dir != tc.wantDir {
				t.Fatalf("got %+v, want cmd=%q dir=%q", got, tc.wantCmd, tc.wantDir)
			}
		})
	}
}

func TestResolveExecDir(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// An empty dir keeps the run's working directory.
	if got, err := resolveExecDir("/some/cwd", ""); err != nil || got != "/some/cwd" {
		t.Fatalf("empty: got (%q, %v), want /some/cwd", got, err)
	}
	// A relative dir is joined onto the working directory.
	if got, err := resolveExecDir(base, "sub"); err != nil || got != sub {
		t.Fatalf("relative: got (%q, %v), want %q", got, err, sub)
	}
	// An absolute dir is used as given.
	if got, err := resolveExecDir(base, sub); err != nil || got != sub {
		t.Fatalf("absolute: got (%q, %v), want %q", got, err, sub)
	}
	// A missing or non-directory path is an error, not a silent fallback.
	if _, err := resolveExecDir(base, "nope"); err == nil {
		t.Error("a missing dir was accepted")
	}
	if _, err := resolveExecDir(base, "file"); err == nil {
		t.Error("a non-directory was accepted")
	}
}

// The execute tool runs a command in the payload's dir without a cd prefix, and
// records where it ran on the tool call.
func TestExecuteRunsInPayloadDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell command")
	}
	dir := t.TempDir()
	obs := &fakeObserver{}
	rt := &Runtime{Observer: obs, Prompter: stubPrompter{}, Logger: nopLogger{}}
	// `pwd` is trusted, so no confirmation is needed.
	cfg := &config.UserConfig{TrustedCmds: []string{"pwd"}}

	payload, err := json.Marshal(execPayload{Cmd: "pwd", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	out, err := runExecute(context.Background(), cfg, rt, "where am I", payload)
	if err != nil {
		t.Fatal(err)
	}
	// The shell may report the path as given or with a symlinked temp dir
	// resolved; accept either.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}
	if !strings.Contains(out, dir) && !strings.Contains(out, resolved) {
		t.Fatalf("command did not run in %q:\n%s", dir, out)
	}
	if n := len(obs.toolCalls); n != 1 || obs.toolCalls[0].Dir != dir {
		t.Fatalf("ToolCall.Dir = %+v, want %q", obs.toolCalls, dir)
	}
}

func TestConfirmTitle(t *testing.T) {
	cases := []struct {
		name string
		verb string
		cmd  string
		want string
	}{
		{"single command is unchanged", "Execute this command?", "df -h", "Execute this command?"},
		{"chains report their command count", "Execute this command?", "a && b", "Execute this command? (2 commands)"},
		{"pipes count too", "Run on nb?", "a | b | c", "Run on nb? (3 commands)"},
		{"a quoted operator is not a separator", "Execute this command?", `grep 'a|b' f`, "Execute this command?"},
		{"backgrounding counts", "Execute this command?", "a & b", "Execute this command? (2 commands)"},
		{"empty command", "Execute this command?", "", "Execute this command?"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := confirmTitle(tc.verb, tc.cmd); got != tc.want {
				t.Errorf("confirmTitle(%q, %q) = %q, want %q", tc.verb, tc.cmd, got, tc.want)
			}
		})
	}
}
