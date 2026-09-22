package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/config"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The nearest directory wins, so running from a subdirectory still finds the
// repository's instructions rather than nothing.
func TestFindContextFileNearestWins(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	write(t, filepath.Join(root, "AGENTS.md"), "root instructions")
	write(t, filepath.Join(root, "a", "AGENTS.md"), "nearer instructions")

	content, path := findContextFile(sub, []string{"AGENTS.md", "CLAUDE.md"})
	if content != "nearer instructions" {
		t.Errorf("content = %q, want the nearest file's", content)
	}
	if want := filepath.Join(root, "a", "AGENTS.md"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

// Names are tried in order within a directory.
func TestFindContextFileNameOrder(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "CLAUDE.md"), "claude instructions")
	write(t, filepath.Join(dir, "AGENTS.md"), "agents instructions")

	content, path := findContextFile(dir, []string{"AGENTS.md", "CLAUDE.md"})
	if content != "agents instructions" {
		t.Errorf("content = %q, want AGENTS.md to win", content)
	}
	if filepath.Base(path) != "AGENTS.md" {
		t.Errorf("path = %q", path)
	}

	// With CLAUDE.md preferred, that one wins instead.
	if _, path := findContextFile(dir, []string{"CLAUDE.md", "AGENTS.md"}); filepath.Base(path) != "CLAUDE.md" {
		t.Errorf("path = %q, want CLAUDE.md", path)
	}
}

func TestFindContextFileAbsent(t *testing.T) {
	// A name that cannot exist keeps this independent of whatever lives in the
	// ancestors of the temp directory.
	content, path := findContextFile(t.TempDir(), []string{"PAI-test-not-present.md"})
	if content != "" || path != "" {
		t.Errorf("got (%q, %q), want empty", content, path)
	}
}

func TestCapContext(t *testing.T) {
	short := capContext("  hello  \n")
	if short != "hello" {
		t.Errorf("short content = %q, want trimmed", short)
	}

	long := capContext(strings.Repeat("x", maxContextFileBytes+100))
	if !strings.Contains(long, "[truncated 100 bytes") {
		t.Errorf("long content should be marked as truncated")
	}
	if len(long) > maxContextFileBytes+80 {
		t.Errorf("long content not actually capped: %d bytes", len(long))
	}
}

// Project instructions belong after the role's own guidance but before the tool
// reference, and must be marked as subordinate.
func TestComposeHeadPlacesProjectContext(t *testing.T) {
	rp := &RolePrompt{
		Name:           "coder",
		Intro:          "You are a coder.",
		Tools:          []ToolSpec{{Name: "execute", Description: "run a command"}},
		ProjectContext: "Always run make fmt.",
		ContextSource:  "/repo/AGENTS.md",
	}
	head := composeHead(rp)

	if !strings.Contains(head, "## Project instructions (from AGENTS.md)") {
		t.Errorf("head is missing the labelled context section:\n%s", head)
	}
	if !strings.Contains(head, "Always run make fmt.") {
		t.Error("head is missing the context contents")
	}
	if !strings.Contains(head, "never override") {
		t.Error("context must be marked as subordinate to the rules")
	}

	intro := strings.Index(head, "You are a coder.")
	ctx := strings.Index(head, "## Project instructions")
	tool := strings.Index(head, "## Tool: execute")
	if !(intro < ctx && ctx < tool) {
		t.Errorf("wrong ordering: intro=%d context=%d tool=%d", intro, ctx, tool)
	}
}

// No context file means no section, and no stray heading.
func TestComposeHeadWithoutProjectContext(t *testing.T) {
	rp := &RolePrompt{Name: "devops", Intro: "You are an SRE."}
	if head := composeHead(rp); strings.Contains(head, "Project instructions") {
		t.Errorf("unexpected context section:\n%s", head)
	}
}

// The role declaration is honoured: whatever the coder role resolves at the
// current working directory is what lands in the prompt.
func TestLoadRolePromptWiresContextFiles(t *testing.T) {
	rp, err := LoadRolePrompt("coder", config.CustomPrompt{})
	if err != nil {
		t.Fatalf("load coder: %v", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	wantContent, wantPath := findContextFile(wd, []string{"AGENTS.md", "CLAUDE.md"})
	if rp.ContextSource != wantPath || rp.ProjectContext != wantContent {
		t.Errorf("context = (%q, %q), want (%q, %q)", rp.ContextSource, rp.ProjectContext, wantPath, wantContent)
	}
}

// A role that declares no context files never reads any.
func TestLoadRolePromptWithoutContextFiles(t *testing.T) {
	rp, err := LoadRolePrompt("devops", config.CustomPrompt{})
	if err != nil {
		t.Fatalf("load devops: %v", err)
	}
	if rp.ContextSource != "" || rp.ProjectContext != "" {
		t.Errorf("devops picked up context: (%q, %q)", rp.ContextSource, rp.ProjectContext)
	}
}
