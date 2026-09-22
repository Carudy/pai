package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readBack(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSetScalarReplacesExistingKey(t *testing.T) {
	path := writeTemp(t, `# leading comment
[app]
default_role = "devops"  # keep me
interactive = true

[session]
persist = false
`)
	if err := SetScalar(path, "app", "default_role", `"coder"`); err != nil {
		t.Fatal(err)
	}
	got := readBack(t, path)
	want := `# leading comment
[app]
default_role = "coder"
interactive = true

[session]
persist = false
`
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestSetScalarInsertsMissingKeyIntoExistingSection(t *testing.T) {
	path := writeTemp(t, `[app]
default_role = "devops"
`)
	if err := SetScalar(path, "app", "default_model", `"openai:gpt-4o"`); err != nil {
		t.Fatal(err)
	}
	got := readBack(t, path)
	want := `[app]
default_model = "openai:gpt-4o"
default_role = "devops"
`
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestSetScalarAppendsMissingSection(t *testing.T) {
	path := writeTemp(t, `[app]
interactive = false
`)
	if err := SetScalar(path, "session", "persist", "true"); err != nil {
		t.Fatal(err)
	}
	got := readBack(t, path)
	want := `[app]
interactive = false

[session]
persist = true
`
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestSetScalarDoesNotTouchOtherSections(t *testing.T) {
	path := writeTemp(t, `[app]
streaming = false

[session]
persist = false
max_turns = 10
`)
	// `persist` exists in [session]; setting it must not touch [app]'s lines.
	if err := SetScalar(path, "session", "persist", "true"); err != nil {
		t.Fatal(err)
	}
	got := readBack(t, path)
	want := `[app]
streaming = false

[session]
persist = true
max_turns = 10
`
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestSetScalarCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	if err := SetScalar(path, "app", "default_role", `"devops"`); err != nil {
		t.Fatal(err)
	}
	got := readBack(t, path)
	want := "\n[app]\ndefault_role = \"devops\"\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestUnsetScalarRemovesKey(t *testing.T) {
	path := writeTemp(t, `[app]
default_model = "x"
default_role = "devops"

[session]
persist = true
`)
	if err := UnsetScalar(path, "app", "default_model"); err != nil {
		t.Fatal(err)
	}
	got := readBack(t, path)
	want := `[app]
default_role = "devops"

[session]
persist = true
`
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestUnsetScalarAbsentKeyIsNoOp(t *testing.T) {
	body := `[app]
default_role = "devops"
`
	path := writeTemp(t, body)
	if err := UnsetScalar(path, "app", "streaming"); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, path); got != body {
		t.Errorf("got:\n%q\nwant:\n%q", got, body)
	}
}

func TestUnsetScalarMissingSectionIsNoOp(t *testing.T) {
	body := `[app]
default_role = "devops"
`
	path := writeTemp(t, body)
	if err := UnsetScalar(path, "session", "persist"); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, path); got != body {
		t.Errorf("got:\n%q\nwant:\n%q", got, body)
	}
}

// A commented-out key must never be treated as the live key.
func TestSetScalarIgnoresCommentedKey(t *testing.T) {
	path := writeTemp(t, `[app]
# default_role = "old"
interactive = false
`)
	if err := SetScalar(path, "app", "default_role", `"coder"`); err != nil {
		t.Fatal(err)
	}
	got := readBack(t, path)
	want := `[app]
default_role = "coder"
# default_role = "old"
interactive = false
`
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}
