package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func assertParses(t *testing.T, body string) {
	t.Helper()
	var v map[string]any
	if _, err := toml.Decode(body, &v); err != nil {
		t.Fatalf("edited file is not valid TOML: %v\n%s", err, body)
	}
}

// A hand-formatted multi-line array must be replaced whole: leaving its
// continuation lines behind produced invalid TOML (the reported corruption).
func TestSetScalarReplacesMultiLineArray(t *testing.T) {
	path := writeTemp(t, `[tool]
trusted_cmds = [
    "ls", "cat",
]
confirm_read = false
`)
	if err := SetScalar(path, "tool", "trusted_cmds", `["ls", "cat", "pwd"]`); err != nil {
		t.Fatal(err)
	}
	got := readBack(t, path)
	want := `[tool]
trusted_cmds = ["ls", "cat", "pwd"]
confirm_read = false
`
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
	assertParses(t, got)
}

// A multi-line array key that is removed must take its body with it.
func TestUnsetScalarRemovesMultiLineArray(t *testing.T) {
	path := writeTemp(t, `[tool]
trusted_cmds = [
    "ls",
    "cat",
]
confirm_read = false
`)
	if err := UnsetScalar(path, "tool", "trusted_cmds"); err != nil {
		t.Fatal(err)
	}
	got := readBack(t, path)
	want := `[tool]
confirm_read = false
`
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
	assertParses(t, got)
}

// Multi-line basic and literal strings are consumed whole too.
func TestSetScalarReplacesMultiLineString(t *testing.T) {
	path := writeTemp(t, "[app]\nprompt = \"\"\"\nline one\n# not a comment\n\"\"\"\ninteractive = true\n")
	if err := SetScalar(path, "app", "prompt", `"short"`); err != nil {
		t.Fatal(err)
	}
	got := readBack(t, path)
	want := "[app]\nprompt = \"short\"\ninteractive = true\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
	assertParses(t, got)
}

// The end-to-end path: appending to a multi-line trusted_cmds keeps the file valid.
func TestAddTrustedCmdsMultiLine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgPath := filepath.Join(dir, "pai", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[tool]\ntrusted_cmds = [\n    \"ls\", \"cat\",\n]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddTrustedCmds("pwd"); err != nil {
		t.Fatal(err)
	}
	got := readBack(t, cfgPath)
	var raw tomlConfig
	if _, err := toml.Decode(got, &raw); err != nil {
		t.Fatalf("invalid TOML after edit: %v\n%s", err, got)
	}
	found := false
	for _, n := range raw.Tool.TrustedCmds {
		if n == "pwd" {
			found = true
		}
	}
	if !found {
		t.Fatalf("pwd not added:\n%s", got)
	}
}
