package prompts

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func userToolsDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "pai", "tools")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(filepath.Dir(dir)))
	return dir
}

func writeTool(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name+".toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const demoCLI = `
name        = "demo"
brief       = "demo tool"
kind        = "cli"
description = "does demo things"

[params]
dataset = { type = "string", required = true, description = "dataset" }
folds   = { type = "integer", default = 5, min = 2, max = 10 }

[cli]
command = "echo"
args    = ["--dataset", "{dataset}", "--folds", "{folds}"]
dir     = "."
timeout_seconds = 5
`

func TestResolveBuiltinsPreserveRoleOrder(t *testing.T) {
	isolateConfig(t)
	set, err := ResolveTools(ResolveOptions{RoleTools: []string{"read", "execute"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Names(); !slices.Equal(got, []string{"read", "execute"}) {
		t.Fatalf("order = %v", got)
	}
	execute := set.ByName["execute"]
	if execute.Kind != ToolBuiltin || execute.Layer != LayerBuiltin {
		t.Fatalf("execute = %+v", execute)
	}
	if execute.Grant.Unit != "command" || !execute.Grant.Persistent || !execute.Grant.TrustAll {
		t.Fatalf("execute grant = %+v", execute.Grant)
	}
	if execute.DefinitionHash == "" {
		t.Fatal("missing definition hash")
	}
	read := set.ByName["read"]
	if read.Grant.Unit != "path" {
		t.Fatalf("read grant = %+v", read.Grant)
	}
}

func TestResolveUserCLITool(t *testing.T) {
	dir := userToolsDir(t)
	path := writeTool(t, dir, "demo", demoCLI)

	set, err := ResolveTools(ResolveOptions{RoleTools: []string{"demo"}})
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := set.Get("demo")
	if !ok {
		t.Fatal("demo not in effective set")
	}
	if tool.Kind != ToolCLI || tool.Layer != LayerUser || tool.Source != path {
		t.Fatalf("tool = %+v", tool)
	}
	if tool.Grant.Unit != "tool" || tool.Grant.Key != "demo" || !tool.Grant.Persistent || !tool.Grant.TrustAll {
		t.Fatalf("grant = %+v", tool.Grant)
	}
	if len(tool.Params) != 2 || tool.Params[0].Name != "dataset" || tool.Params[1].Name != "folds" {
		t.Fatalf("params = %+v", tool.Params)
	}
	const wantSchema = `{"dataset":"<string, required>","folds":<integer=5, 2..10>}`
	if tool.Schema != wantSchema {
		t.Fatalf("schema = %q, want %q", tool.Schema, wantSchema)
	}
	if tool.DefinitionHash == "" {
		t.Fatal("missing definition hash")
	}
}

func TestResolveRoleExtraToolsAppend(t *testing.T) {
	dir := userToolsDir(t)
	writeTool(t, dir, "demo", demoCLI)

	set, err := ResolveTools(ResolveOptions{
		RoleTools:      []string{"read"},
		RoleExtraTools: []string{"demo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Names(); !slices.Equal(got, []string{"read", "demo"}) {
		t.Fatalf("order = %v", got)
	}
}

func TestResolveDedupesRepeatedNames(t *testing.T) {
	isolateConfig(t)
	set, err := ResolveTools(ResolveOptions{
		RoleTools:      []string{"read", "read"},
		RoleExtraTools: []string{"read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Names(); !slices.Equal(got, []string{"read"}) {
		t.Fatalf("names = %v", got)
	}
}

func TestUserToolCannotShadowBuiltin(t *testing.T) {
	dir := userToolsDir(t)
	writeTool(t, dir, "execute", `
name = "execute"
brief = "shadow"
kind = "cli"
description = "shadow"
[cli]
command = "echo"
`)
	_, err := ResolveTools(ResolveOptions{RoleTools: []string{"execute"}})
	if err == nil || !strings.Contains(err.Error(), "shadows") {
		t.Fatalf("err = %v", err)
	}
}

func TestUserToolNameMustMatchFilename(t *testing.T) {
	dir := userToolsDir(t)
	writeTool(t, dir, "demo", strings.Replace(demoCLI, `name        = "demo"`, `name        = "other"`, 1))
	_, err := ResolveTools(ResolveOptions{RoleTools: []string{"demo"}})
	if err == nil || !strings.Contains(err.Error(), "must match filename") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownTool(t *testing.T) {
	isolateConfig(t)
	_, err := ResolveTools(ResolveOptions{RoleTools: []string{"missing"}})
	if err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownPlaceholder(t *testing.T) {
	dir := userToolsDir(t)
	writeTool(t, dir, "demo", strings.Replace(demoCLI, `"{folds}"`, `"{missing}"`, 1))
	_, err := ResolveTools(ResolveOptions{RoleTools: []string{"demo"}})
	if err == nil || !strings.Contains(err.Error(), "unknown parameter") {
		t.Fatalf("err = %v", err)
	}
}

func TestProjectToolsRequireTrust(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	toolsDir := filepath.Join(workspace, ".pai", "tools")
	if err := os.MkdirAll(toolsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTool(t, toolsDir, "demo", demoCLI)

	// Untrusted: the project definition is invisible, and the role still works.
	set, err := ResolveTools(ResolveOptions{RoleTools: []string{"read"}, Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if set.Has("demo") {
		t.Fatal("an untrusted project contributed a tool")
	}

	// Trusted: the project tool is appended after the role's own tools.
	set, err = ResolveTools(ResolveOptions{
		RoleTools:       []string{"read"},
		Workspace:       workspace,
		TrustedProjects: []string{root},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Names(); !slices.Equal(got, []string{"read", "demo"}) {
		t.Fatalf("names = %v", got)
	}
	demo, ok := set.Get("demo")
	if !ok || demo.Layer != LayerProject {
		t.Fatalf("demo = %+v", demo)
	}
	if demo.Grant.Persistent || demo.Grant.TrustAll {
		t.Fatalf("project grant = %+v, want session-only and not trust-all", demo.Grant)
	}

	resolved, err := ResolveToolIn("demo", workspace, []string{root})
	if err != nil || resolved.Layer != LayerProject {
		t.Fatalf("ResolveToolIn = %+v, %v", resolved, err)
	}
	names, err := ProjectToolNames(workspace, []string{root})
	if err != nil || !slices.Equal(names, []string{"demo"}) {
		t.Fatalf("ProjectToolNames = %v, %v", names, err)
	}
}

func TestProjectToolsExcludedOutsideRoot(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	trusted := filepath.Join(root, "trusted")
	other := filepath.Join(root, "other")
	toolsDir := filepath.Join(other, ".pai", "tools")
	if err := os.MkdirAll(toolsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTool(t, toolsDir, "demo", demoCLI)

	set, err := ResolveTools(ResolveOptions{
		RoleTools:       []string{"read"},
		Workspace:       other,
		TrustedProjects: []string{trusted},
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.Has("demo") {
		t.Fatal("a workspace outside the trusted root loaded project tools")
	}
}

func TestProjectToolCollisions(t *testing.T) {
	t.Run("built-in", func(t *testing.T) {
		isolateConfig(t)
		root := t.TempDir()
		workspace := filepath.Join(root, "repo")
		toolsDir := filepath.Join(workspace, ".pai", "tools")
		if err := os.MkdirAll(toolsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeTool(t, toolsDir, "execute", strings.Replace(demoCLI, `name        = "demo"`, `name        = "execute"`, 1))
		_, err := ResolveTools(ResolveOptions{RoleTools: []string{"read"}, Workspace: workspace, TrustedProjects: []string{root}})
		if err == nil || !strings.Contains(err.Error(), "shadows") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("user", func(t *testing.T) {
		userDir := userToolsDir(t)
		writeTool(t, userDir, "demo", demoCLI)
		root := t.TempDir()
		workspace := filepath.Join(root, "repo")
		toolsDir := filepath.Join(workspace, ".pai", "tools")
		if err := os.MkdirAll(toolsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeTool(t, toolsDir, "demo", demoCLI)
		_, err := ResolveTools(ResolveOptions{RoleTools: []string{"read"}, Workspace: workspace, TrustedProjects: []string{root}})
		if err == nil || !strings.Contains(err.Error(), "collides with a user tool") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestProjectToolSymlinkEscapesAreRejected(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	toolsDir := filepath.Join(workspace, ".pai", "tools")
	if err := os.MkdirAll(toolsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.toml")
	if err := os.WriteFile(outside, []byte(demoCLI), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(toolsDir, "demo.toml")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := ResolveTools(ResolveOptions{RoleTools: []string{"read"}, Workspace: workspace, TrustedProjects: []string{root}})
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("err = %v", err)
	}
}

func TestProjectToolsDirSymlinkEscapeIsRejected(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(workspace, ".pai"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "demo.toml"), []byte(demoCLI), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, ".pai", "tools")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := ResolveTools(ResolveOptions{RoleTools: []string{"read"}, Workspace: workspace, TrustedProjects: []string{root}})
	if err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("err = %v", err)
	}
}

func TestDefinitionHashIgnoresCommentsAndWhitespace(t *testing.T) {
	dir := userToolsDir(t)
	writeTool(t, dir, "demo", demoCLI)
	first, err := ResolveTools(ResolveOptions{RoleTools: []string{"demo"}})
	if err != nil {
		t.Fatal(err)
	}
	withComments := "# a comment\n\n" + strings.ReplaceAll(demoCLI, "\n", "\n\n")
	writeTool(t, dir, "demo", withComments)
	second, err := ResolveTools(ResolveOptions{RoleTools: []string{"demo"}})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := first.Get("demo")
	b, _ := second.Get("demo")
	if a.DefinitionHash != b.DefinitionHash {
		t.Fatalf("hash changed: %s != %s", a.DefinitionHash, b.DefinitionHash)
	}
}

func TestHTTPToolShape(t *testing.T) {
	dir := userToolsDir(t)
	writeTool(t, dir, "search", `
name = "search"
brief = "search an API"
kind = "http"
description = "search"
[params]
query = { type = "string", required = true }
[http]
method = "GET"
url    = "https://api.example.com/search?q={query}"
max_bytes = 1024
`)
	set, err := ResolveTools(ResolveOptions{RoleTools: []string{"search"}})
	if err != nil {
		t.Fatal(err)
	}
	tool, _ := set.Get("search")
	if tool.Kind != ToolHTTP || tool.HTTP == nil || tool.HTTP.MaxBytes != 1024 {
		t.Fatalf("tool = %+v", tool)
	}
}

func TestHTTPHostCannotBeAParameter(t *testing.T) {
	dir := userToolsDir(t)
	writeTool(t, dir, "search", `
name = "search"
brief = "search an API"
kind = "http"
description = "search"
[params]
host = { type = "string", required = true }
[http]
method = "GET"
url    = "https://{host}/search"
`)
	_, err := ResolveTools(ResolveOptions{RoleTools: []string{"search"}})
	if err == nil || !strings.Contains(err.Error(), "scheme/host") {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPDefinitionValidation(t *testing.T) {
	cases := []struct {
		name string
		http string
		want string
	}{
		{
			name: "header name",
			http: "[http]\nmethod = \"GET\"\nurl = \"https://api.example.com/x\"\nheaders = { \"bad name\" = \"x\" }\n",
			want: "not a valid token",
		},
		{
			name: "header newline",
			http: "[http]\nmethod = \"GET\"\nurl = \"https://api.example.com/x\"\nheaders = { X = \"a\\nb\" }\n",
			want: "newline",
		},
		{
			name: "body collision",
			http: "[http]\nmethod = \"POST\"\nurl = \"https://api.example.com/x\"\nbody_params = [\"query\"]\nbody_extra = { query = \"fixed\" }\n",
			want: "collides",
		},
		{
			name: "body optional without default",
			http: "[http]\nmethod = \"POST\"\nurl = \"https://api.example.com/x\"\nbody_params = [\"opt\"]\n",
			want: "required or have a default",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := userToolsDir(t)
			body := "name = \"search\"\nbrief = \"search\"\nkind = \"http\"\n[params]\nquery = { type = \"string\", required = true }\nopt = { type = \"string\" }\n" + tc.http
			writeTool(t, dir, "search", body)
			_, err := ResolveTools(ResolveOptions{RoleTools: []string{"search"}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParamValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "required with default",
			body: strings.Replace(demoCLI, `dataset = { type = "string", required = true, description = "dataset" }`, `dataset = { type = "string", required = true, default = "x" }`, 1),
			want: "mutually exclusive",
		},
		{
			name: "unknown type",
			body: strings.Replace(demoCLI, `type = "integer"`, `type = "bogus"`, 1),
			want: "unknown type",
		},
		{
			name: "enum without values",
			body: strings.Replace(demoCLI, `dataset = { type = "string", required = true, description = "dataset" }`, `dataset = { type = "enum", required = true }`, 1),
			want: "enum needs values",
		},
		{
			name: "bad default type",
			body: strings.Replace(demoCLI, `folds   = { type = "integer", default = 5, min = 2, max = 10 }`, `folds   = { type = "integer", default = "five" }`, 1),
			want: "default must be an integer",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := userToolsDir(t)
			writeTool(t, dir, "demo", tc.body)
			_, err := ResolveTools(ResolveOptions{RoleTools: []string{"demo"}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
