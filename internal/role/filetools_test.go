package role

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/config"
)

// stubPrompter answers every confirmation the same way.
type stubPrompter struct{ ok bool }

func (p stubPrompter) Ask(string) (string, error)   { return "", nil }
func (p stubPrompter) Confirm(string) (bool, error) { return p.ok, nil }

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func editPayloadJSON(t *testing.T, p editPayload) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testEditRuntime(ok bool) *Runtime {
	return &Runtime{Observer: &fakeObserver{}, Prompter: stubPrompter{ok: ok}, Logger: nopLogger{}}
}

// A unique match is replaced and the rest of the file is left untouched.
func TestRunEditReplacesUniqueMatch(t *testing.T) {
	path := writeTemp(t, "alpha\nbeta\ngamma\n")
	rt := testEditRuntime(true)

	out, err := runEdit(context.Background(), &config.UserConfig{}, rt, "r",
		editPayloadJSON(t, editPayload{Path: path, OldString: "beta", NewString: "BETA"}))
	if err != nil {
		t.Fatalf("runEdit: %v", err)
	}
	if !strings.Contains(out, "[edit result]") {
		t.Errorf("unexpected observation: %q", out)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "alpha\nBETA\ngamma\n" {
		t.Errorf("file = %q", got)
	}
}

// An ambiguous match is refused so a stale edit cannot silently hit the wrong
// occurrence.
func TestRunEditRejectsAmbiguousMatch(t *testing.T) {
	path := writeTemp(t, "dup\nother\ndup\n")
	rt := testEditRuntime(true)

	_, err := runEdit(context.Background(), &config.UserConfig{}, rt, "r",
		editPayloadJSON(t, editPayload{Path: path, OldString: "dup", NewString: "x"}))
	if err == nil || !strings.Contains(err.Error(), "appears 2 times") {
		t.Fatalf("want an ambiguity error, got %v", err)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "dup\nother\ndup\n" {
		t.Errorf("file changed despite the error: %q", got)
	}
}

// replace_all makes an ambiguous match intentional.
func TestRunEditReplaceAll(t *testing.T) {
	path := writeTemp(t, "dup\nother\ndup\n")
	rt := testEditRuntime(true)

	if _, err := runEdit(context.Background(), &config.UserConfig{}, rt, "r",
		editPayloadJSON(t, editPayload{Path: path, OldString: "dup", NewString: "x", ReplaceAll: true})); err != nil {
		t.Fatalf("runEdit: %v", err)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "x\nother\nx\n" {
		t.Errorf("file = %q", got)
	}
}

// A missing match is an error, not a silent no-op.
func TestRunEditNotFound(t *testing.T) {
	path := writeTemp(t, "hello\n")
	rt := testEditRuntime(true)

	_, err := runEdit(context.Background(), &config.UserConfig{}, rt, "r",
		editPayloadJSON(t, editPayload{Path: path, OldString: "absent", NewString: "x"}))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want a not-found error, got %v", err)
	}
}

// Declining the confirmation leaves the file untouched.
func TestRunEditDeclined(t *testing.T) {
	path := writeTemp(t, "hello\n")
	rt := testEditRuntime(false)

	out, err := runEdit(context.Background(), &config.UserConfig{}, rt, "r",
		editPayloadJSON(t, editPayload{Path: path, OldString: "hello", NewString: "bye"}))
	if err != nil {
		t.Fatalf("runEdit: %v", err)
	}
	if !strings.Contains(out, "USER DECLINED") {
		t.Errorf("unexpected observation: %q", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hello\n" {
		t.Errorf("file changed despite decline: %q", got)
	}
}

// The diff marks the removed and added text so the user can review it.
func TestDiffEditMarksChanges(t *testing.T) {
	got := diffEdit("a\nb\nc\n", "b", "B", 1)
	for _, want := range []string{"@@ line 2 @@", "  a", "- b", "+ B", "  c"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff missing %q:\n%s", want, got)
		}
	}
}

// read numbers lines and returns only the requested window, pointing at the
// continuation so the model can fetch the rest.
func TestRunReadWindow(t *testing.T) {
	path := writeTemp(t, "l1\nl2\nl3\nl4\nl5\n")
	rt := &Runtime{Observer: &fakeObserver{}, Prompter: stubPrompter{}, Logger: nopLogger{}}

	raw, _ := json.Marshal(readPayload{Path: path, Offset: 2, Limit: 2})
	out, err := runRead(context.Background(), &config.UserConfig{}, rt, "r", raw)
	if err != nil {
		t.Fatalf("runRead: %v", err)
	}
	if !strings.Contains(out, "     2\tl2") || !strings.Contains(out, "     3\tl3") {
		t.Errorf("missing numbered window:\n%s", out)
	}
	if strings.Contains(out, "l1") || strings.Contains(out, "l4") {
		t.Errorf("window leaked lines outside offset/limit:\n%s", out)
	}
	if !strings.Contains(out, "continues beyond line 3") || !strings.Contains(out, "offset=4") {
		t.Errorf("missing continuation hint:\n%s", out)
	}
}

// A binary file is refused rather than fed to the model.
func TestRunReadRefusesBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(path, []byte("a\x00b"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{Observer: &fakeObserver{}, Prompter: stubPrompter{}, Logger: nopLogger{}}

	raw, _ := json.Marshal(readPayload{Path: path})
	if _, err := runRead(context.Background(), &config.UserConfig{}, rt, "r", raw); err == nil {
		t.Fatal("expected a binary-file error")
	}
}
