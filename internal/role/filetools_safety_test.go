package role

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/tool"
)

type fileApproval struct{ approve func() }

func (p fileApproval) Ask(string) (string, error)   { return "", nil }
func (p fileApproval) Confirm(string) (bool, error) { p.approve(); return true, nil }

type fileTrustApproval struct {
	choice  core.TrustChoice
	approve func()
}

func (p fileTrustApproval) Ask(string) (string, error)   { return "", nil }
func (p fileTrustApproval) Confirm(string) (bool, error) { return false, nil }
func (p fileTrustApproval) ConfirmPath(string, string) (core.TrustChoice, error) {
	p.approve()
	return p.choice, nil
}

func TestRejectedFileMutationDoesNotGrantTrust(t *testing.T) {
	for _, name := range []string{"edit", "write"} {
		for _, choice := range []core.TrustChoice{core.TrustSession, core.TrustPersist} {
			t.Run(name+"/"+string(rune('0'+choice)), func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				root := t.TempDir()
				parent := filepath.Join(root, "parent")
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
				outside := t.TempDir()
				path := filepath.Join(parent, "file")
				if name == "edit" {
					if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				rt := testEditRuntime(true)
				rt.Prompter = fileTrustApproval{choice: choice, approve: func() {
					if err := os.Rename(parent, parent+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, parent); err != nil {
						t.Skipf("symlinks unavailable: %v", err)
					}
				}}
				var err error
				if name == "edit" {
					_, err = runEdit(context.Background(), &config.UserConfig{}, rt, "", editPayloadJSON(t, editPayload{Path: path, OldString: "old", NewString: "new"}))
				} else {
					raw, _ := json.Marshal(writePayload{Path: path, Content: "new"})
					_, err = runWrite(context.Background(), &config.UserConfig{}, rt, "", raw)
				}
				if err == nil {
					t.Fatal("substituted parent accepted")
				}
				if tool.IsTrustedPathAt(filepath.Join(outside, "file"), rt.trustedPaths(&config.UserConfig{}), "") {
					t.Fatal("failed mutation granted session trust")
				}
				cfg, err := config.LoadUserConfig()
				if err != nil {
					t.Fatal(err)
				}
				if len(cfg.TrustedPaths) != 0 {
					t.Fatalf("failed mutation persisted trust: %v", cfg.TrustedPaths)
				}
			})
		}
	}
}

type readRetargetObserver struct {
	fakeObserver
	change func()
}

func (o *readRetargetObserver) ToolCall(core.ToolCall) { o.change() }

func TestRunReadTrustUsesPinnedTarget(t *testing.T) {
	safe := t.TempDir()
	public := filepath.Join(safe, "public")
	if err := os.WriteFile(public, []byte("public"), 0600); err != nil {
		t.Fatal(err)
	}
	secret := writeTemp(t, "private secret")
	link := filepath.Join(safe, "link")
	if err := os.Symlink(public, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	observer := &readRetargetObserver{change: func() {
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, link); err != nil {
			t.Fatal(err)
		}
	}}
	rt := &Runtime{Observer: observer, Prompter: stubPrompter{ok: false}}
	raw, _ := json.Marshal(readPayload{Path: link})
	out, err := runRead(context.Background(), &config.UserConfig{ConfirmRead: true, TrustedPaths: []string{safe}}, rt, "", raw)
	if err == nil || strings.Contains(out, "private secret") {
		t.Fatalf("retargeted read leaked: out=%q err=%v", out, err)
	}
}

func TestRunEditRejectsChangesDuringApproval(t *testing.T) {
	path := writeTemp(t, "original\n")
	rt := testEditRuntime(true)
	rt.Prompter = fileApproval{approve: func() {
		if err := os.WriteFile(path, []byte("manual changes\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}}
	_, err := runEdit(context.Background(), &config.UserConfig{}, rt, "", editPayloadJSON(t, editPayload{Path: path, OldString: "original", NewString: "agent"}))
	if err == nil || !strings.Contains(err.Error(), "changed since capture") {
		t.Fatalf("expected stale edit: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "manual changes\n" {
		t.Fatalf("external changes lost: %q", data)
	}
}

func TestRunWriteNoClobberDuringApproval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.txt")
	rt := testEditRuntime(true)
	rt.Prompter = fileApproval{approve: func() {
		if err := os.WriteFile(path, []byte("manual file"), 0600); err != nil {
			t.Fatal(err)
		}
	}}
	raw, _ := json.Marshal(writePayload{Path: path, Content: "agent file"})
	_, err := runWrite(context.Background(), &config.UserConfig{}, rt, "", raw)
	if err == nil {
		t.Fatal("raced create succeeded")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "manual file" {
		t.Fatalf("external file lost: %q", data)
	}
}

func TestRunEditCancelledDuringApproval(t *testing.T) {
	path := writeTemp(t, "original\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := testEditRuntime(true)
	rt.Prompter = fileApproval{approve: cancel}
	if _, err := runEdit(ctx, &config.UserConfig{}, rt, "", editPayloadJSON(t, editPayload{Path: path, OldString: "original", NewString: "agent"})); err == nil {
		t.Fatal("cancelled edit succeeded")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original\n" {
		t.Fatalf("cancelled edit changed file: %q", data)
	}
}

func TestRunEditBatchAllOrNothing(t *testing.T) {
	for _, tc := range []struct {
		name, payload, want string
		ok                  bool
	}{
		{"disjoint", `{"edits":[{"old_string":"alpha","new_string":"ALPHA"},{"old_string":"gamma","new_string":"GAMMA"}]}`, "ALPHA\nbeta\nGAMMA\n", true},
		{"missing second", `{"edits":[{"old_string":"alpha","new_string":"ALPHA"},{"old_string":"missing","new_string":"GAMMA"}]}`, "alpha\nbeta\ngamma\n", false},
		{"overlap", `{"edits":[{"old_string":"alpha\nbeta","new_string":"ALPHA"},{"old_string":"beta","new_string":"BETA"}]}`, "alpha\nbeta\ngamma\n", false},
		{"missing replacement", `{"edits":[{"old_string":"alpha","new_str":"ALPHA"}]}`, "alpha\nbeta\ngamma\n", false},
		{"mixed shapes", `{"old_string":"alpha","new_string":"ALPHA","edits":[{"old_string":"gamma","new_string":"GAMMA"}]}`, "alpha\nbeta\ngamma\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTemp(t, "alpha\nbeta\ngamma\n")
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.payload), &fields); err != nil {
				t.Fatal(err)
			}
			fields["path"], _ = json.Marshal(path)
			raw, _ := json.Marshal(fields)
			out, err := runEdit(context.Background(), &config.UserConfig{}, testEditRuntime(true), "", raw)
			if (err == nil) != tc.ok {
				t.Fatalf("out=%q err=%v", out, err)
			}
			data, _ := os.ReadFile(path)
			if string(data) != tc.want {
				t.Fatalf("file=%q want=%q", data, tc.want)
			}
		})
	}
}

func TestRunEditCRLFAndBOM(t *testing.T) {
	path := writeTemp(t, "\ufeffalpha\r\nbeta\r\ngamma\r\n")
	raw := editPayloadJSON(t, editPayload{Path: path, OldString: "alpha\nbeta", NewString: "ALPHA\nBETA"})
	out, err := runEdit(context.Background(), &config.UserConfig{}, testEditRuntime(true), "", raw)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "\ufeffALPHA\r\nBETA\r\ngamma\r\n" || !strings.Contains(out, "CRLF/LF") {
		t.Fatalf("file=%q out=%q", data, out)
	}
}

func TestFilePayloadMissingFieldsCannotDelete(t *testing.T) {
	path := writeTemp(t, "original")
	for _, fields := range []map[string]any{{"old_string": "original"}, {"old_string": "original", "new_string": nil}, {"old_string": "original", "new_str": "replacement"}} {
		fields["path"] = path
		raw, _ := json.Marshal(fields)
		if _, err := runEdit(context.Background(), &config.UserConfig{}, testEditRuntime(true), "", raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	raw, _ := json.Marshal(map[string]any{"path": path, "old_string": "original", "new_string": ""})
	if _, err := runEdit(context.Background(), &config.UserConfig{}, testEditRuntime(true), "", raw); err != nil {
		t.Fatalf("explicit deletion refused: %v", err)
	}
	data, _ := os.ReadFile(path)
	if len(data) != 0 {
		t.Fatalf("deletion failed: %q", data)
	}
	newPath := filepath.Join(t.TempDir(), "new")
	raw, _ = json.Marshal(map[string]any{"path": newPath})
	if _, err := runWrite(context.Background(), &config.UserConfig{}, testEditRuntime(true), "", raw); err == nil {
		t.Fatal("missing content created empty file")
	}
}

func TestRunReadBudgetContinuation(t *testing.T) {
	path := writeTemp(t, "first\nsecond\nthird\n")
	cfg := &config.UserConfig{Context: config.ContextConfig{ExecLimit: 13}}
	raw, _ := json.Marshal(readPayload{Path: path})
	out, err := runRead(context.Background(), cfg, testEditRuntime(true), "", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "     1\tfirst\n") || !strings.Contains(out, "offset=2") || strings.Contains(out, "second") || strings.Contains(out, " of ") {
		t.Fatalf("incorrect continuation: %s", out)
	}
}

func TestRunReadEmptyAndOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		content string
		offset  int
		ok      bool
	}{{"", 1, true}, {"a\n", 3, false}} {
		path := writeTemp(t, tc.content)
		raw, _ := json.Marshal(readPayload{Path: path, Offset: tc.offset})
		out, err := runRead(context.Background(), &config.UserConfig{}, testEditRuntime(true), "", raw)
		if (err == nil) != tc.ok {
			t.Fatalf("out=%q err=%v", out, err)
		}
	}
}
