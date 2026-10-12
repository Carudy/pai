package prompts

import (
	"os"
	"path/filepath"
	"slices"

	"testing"

	"github.com/BurntSushi/toml"
)

func TestBuiltinLatexRole(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if !slices.Contains(RoleNames(), "latex") {
		t.Fatal("latex is not discoverable")
	}
	data, err := builtin.ReadFile("roles/latex.toml")
	if err != nil {
		t.Fatal(err)
	}
	var role struct {
		Name         string   `toml:"name"`
		Description  string   `toml:"description"`
		Tools        []string `toml:"tools"`
		ContextFiles []string `toml:"context_files"`
		Intro        string   `toml:"intro"`
	}
	if _, err := toml.Decode(string(data), &role); err != nil {
		t.Fatal(err)
	}
	if role.Name != "latex" || role.Description == "" || role.Intro == "" {
		t.Fatalf("incomplete definition: %+v", role)
	}
	for _, tool := range []string{"read", "edit", "write", "execute", "websearch", "papersearch"} {
		if !slices.Contains(role.Tools, tool) {
			t.Errorf("missing capability %s", tool)
		}
	}
	if slices.Contains(role.Tools, "remote") {
		t.Error("latex should not allow remote execution")
	}
	if !slices.Contains(role.ContextFiles, "AGENTS.md") {
		t.Error("latex should honor manuscript project instructions")
	}
}

func TestUserLatexRoleOverridesBuiltin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir()) // Force the embedded fallback, not source-checkout hot reload.
	_, source, err := ReadRole("latex")
	if err != nil || source != "built-in:roles/latex.toml" {
		t.Fatalf("fallback: source=%q err=%v", source, err)
	}
	dir := UserRolesDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "latex.toml")
	custom := "name = \"latex\"\nintro = \"Personal writing style\"\ntools = [\"read\"]\n"
	if err := os.WriteFile(path, []byte(custom), 0600); err != nil {
		t.Fatal(err)
	}
	data, source, err := ReadRole("latex")
	if err != nil || source != path || string(data) != custom {
		t.Fatalf("override: data=%q source=%q err=%v", data, source, err)
	}
	count := 0
	for _, name := range RoleNames() {
		if name == "latex" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("latex appears %d times", count)
	}

}
