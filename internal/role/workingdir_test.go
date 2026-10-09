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
)

type workspaceObserver struct {
	fakeObserver
	calls []core.ToolCall
}

func (o *workspaceObserver) ToolCall(c core.ToolCall) { o.calls = append(o.calls, c) }

func TestRuntimeWorkspaceFiles(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"one", "two"} {
		dir := t.TempDir()
		t.Run(content, func(t *testing.T) {
			t.Parallel()
			obs := &workspaceObserver{}
			rt := &Runtime{WorkingDir: dir, Observer: obs, Prompter: stubPrompter{ok: false}}
			cfg := &config.UserConfig{TrustedPaths: []string{"."}, ConfirmRead: true}
			payload, _ := json.Marshal(writePayload{Path: "file", Content: content})
			if _, err := runWrite(context.Background(), cfg, rt, "", payload); err != nil {
				t.Fatal(err)
			}
			payload, _ = json.Marshal(readPayload{Path: "file"})
			out, err := runRead(context.Background(), cfg, rt, "", payload)
			if err != nil || !strings.Contains(out, content) {
				t.Fatalf("read=%q err=%v", out, err)
			}
			payload, _ = json.Marshal(editPayload{Path: "file", OldString: content, NewString: content + " edited"})
			if _, err := runEdit(context.Background(), cfg, rt, "", payload); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "file"))
			if err != nil || string(data) != content+" edited" {
				t.Fatalf("file=%q err=%v", data, err)
			}
			for _, call := range obs.calls {
				if call.Target != filepath.Join(dir, "file") || !call.Trusted {
					t.Fatalf("wrong target/trust: %+v", call)
				}
			}
			if got, _ := os.Getwd(); got != cwd {
				t.Fatalf("cwd changed: %s", got)
			}
		})
	}
}

func TestRoleSwitchUsesRuntimeWorkspace(t *testing.T) {
	cc, rt, _ := newTestCtx(t)
	rt.WorkingDir = t.TempDir()
	path := filepath.Join(rt.WorkingDir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("runtime instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	runRole(cc, []string{"coder"})
	if cc.rp.ContextSource != path || cc.rp.ProjectContext != "runtime instructions" {
		t.Fatalf("wrong role context: %+v", cc.rp)
	}
	if !strings.Contains(cc.rp.Messages(nil)[0].Content, "Working Dir: "+rt.WorkingDir+"\n") {
		t.Fatal("role switch lost workspace")
	}
}
