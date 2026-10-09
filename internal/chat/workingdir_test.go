package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/config"
)

func TestLoadRolePromptAtWorkspaces(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, instructions := range []string{"workspace one", "workspace two"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(instructions), 0600); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(dir, "sub")
		if err := os.Mkdir(sub, 0700); err != nil {
			t.Fatal(err)
		}
		t.Run(instructions, func(t *testing.T) {
			t.Parallel()
			rp, err := LoadRolePromptAt("coder", config.CustomPrompt{}, sub)
			if err != nil {
				t.Fatal(err)
			}
			if rp.ProjectContext != instructions || rp.ContextSource != filepath.Join(dir, "AGENTS.md") {
				t.Fatalf("wrong context: %+v", rp)
			}
			head := rp.Messages(nil)[0].Content
			if !strings.Contains(head, "Working Dir: "+sub+"\n") {
				t.Fatalf("missing workspace in head: %s", head)
			}
			if head != rp.Messages(nil)[0].Content {
				t.Fatal("head changed")
			}
			if got, _ := os.Getwd(); got != cwd {
				t.Fatalf("process cwd changed: %s", got)
			}
		})
	}
}
