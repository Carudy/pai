package role

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPlanFileEditsOriginalSnapshot(t *testing.T) {
	plan, err := planFileEdits("alpha beta\n", []fileEdit{
		{OldString: "alpha", NewString: "beta"},
		{OldString: "beta", NewString: "gamma"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Content != "beta gamma\n" || plan.Replacements != 2 || plan.FirstLine != 1 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	for _, line := range []string{"- alpha beta", "+ beta gamma"} {
		if !strings.Contains(plan.Diff, line) {
			t.Errorf("diff missing %q: %s", line, plan.Diff)
		}
	}
	_, err = planFileEdits("alpha\n", []fileEdit{
		{OldString: "alpha", NewString: "beta"},
		{OldString: "beta", NewString: "gamma"},
	})
	if err == nil || !strings.Contains(err.Error(), "edit 2") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("replacement text was searched: %v", err)
	}
}

func TestPlanFileEditsRejectsInvalid(t *testing.T) {
	tests := []struct {
		name, content string
		edits         []fileEdit
		want          string
	}{
		{"empty batch", "text", nil, "1–50"},
		{"empty query", "text", []fileEdit{{NewString: "x"}}, "edit 1 requires a non-empty"},
		{"identical", "text", []fileEdit{{OldString: "text", NewString: "text"}}, "identical"},
		{"missing", "text", []fileEdit{{OldString: "other", NewString: "x"}}, "not found"},
		{"ambiguous", "dup\nother\ndup\n", []fileEdit{{OldString: "dup", NewString: "x"}}, "appears 2 times (lines 1, 3)"},
		{"self overlap unique", "aaaaa", []fileEdit{{OldString: "aaaa", NewString: "x"}}, "appears 2 times"},
		{"self overlap all", "aaaaa", []fileEdit{{OldString: "aaaa", NewString: "x", ReplaceAll: true}}, "overlapping"},
		{"normalized self overlap", "\r\n\r\n\r\n", []fileEdit{{OldString: "\n\n", NewString: "x", ReplaceAll: true}}, "overlapping"},
		{"batch overlap", "abcdef", []fileEdit{{OldString: "abcd", NewString: "x"}, {OldString: "cdef", NewString: "y"}}, "edit 2 at line 1 overlaps edit 1 at line 1"},
		{"same range", "abc", []fileEdit{{OldString: "abc", NewString: "x"}, {OldString: "abc", NewString: "y"}}, "overlaps"},
		{"contained range", "abcdef", []fileEdit{{OldString: "abcdef", NewString: "x"}, {OldString: "cd", NewString: "y"}}, "overlaps"},
		{"invalid content", "x\xff", []fileEdit{{OldString: "x", NewString: "y"}}, "invalid UTF-8"},
		{"NUL content", "x\x00", []fileEdit{{OldString: "x", NewString: "y"}}, "NUL"},
		{"invalid old", "text", []fileEdit{{OldString: "\xff", NewString: "y"}}, "edit 1 old_string: invalid UTF-8"},
		{"invalid new", "text", []fileEdit{{OldString: "text", NewString: "\xff"}}, "edit 1 new_string: invalid UTF-8"},
		{"NUL old", "text", []fileEdit{{OldString: "\x00", NewString: "y"}}, "old_string: contains NUL"},
		{"NUL new", "text", []fileEdit{{OldString: "text", NewString: "\x00"}}, "new_string: contains NUL"},
		{"whitespace", "a  b", []fileEdit{{OldString: "a b", NewString: "x"}}, "not found"},
		{"unicode", "é", []fileEdit{{OldString: "e\u0301", NewString: "x"}}, "not found"},
		{"bare CR", "a\rb", []fileEdit{{OldString: "a\nb", NewString: "x"}}, "not found"},
		{"normalized no-op", "a\r\nb\r\n", []fileEdit{{OldString: "a\nb", NewString: "a\r\nb"}}, "identical after"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := planFileEdits(tt.content, tt.edits)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
			if plan != (editPlan{}) {
				t.Fatalf("error returned a partial plan: %+v", plan)
			}
		})
	}
}

func TestPlanFileEditsLineEndings(t *testing.T) {
	tests := []struct {
		name, content, old, new, want string
		adjusted                      bool
	}{
		{"CRLF fallback", "before\r\na\r\nb\r\nafter\r\n", "a\nb", "A\nB", "before\r\nA\r\nB\r\nafter\r\n", true},
		{"LF fallback", "before\na\nb\nafter\n", "a\r\nb", "A\r\nB", "before\nA\nB\nafter\n", true},
		{"uniform CRLF insertion", "a\r\nb\r\n", "a", "A\nmore", "A\r\nmore\r\nb\r\n", true},
		{"mixed CRLF range", "head\na\r\nb\r\ntail\n", "a\nb", "A\nB", "head\nA\r\nB\r\ntail\n", true},
		{"mixed LF range", "head\r\na\nb\ntail\r\n", "a\r\nb", "A\r\nB", "head\r\nA\nB\ntail\r\n", true},
		{"mixed range preserved", "a\r\nb\nc", "a\r\nb\nc", "A\nB\r\nC", "A\nB\r\nC", false},
		{"mixed single line preserved", "a\r\nb\n", "a", "A\nmore", "A\nmore\r\nb\n", false},
		{"exact takes priority", "a\nb\na\r\nb\r\n", "a\nb", "A\nB", "A\nB\na\r\nb\r\n", false},
		{"BOM preserved", "\ufeffa\r\nb\r\n", "a\nb", "A\nB", "\ufeffA\r\nB\r\n", true},
		{"unicode offsets", "☃\r\na\r\nb\r\n終\r\n", "a\nb", "字\n文", "☃\r\n字\r\n文\r\n終\r\n", true},
		{"normalized boundary", "a\r\nb", "a\n", "A\n", "A\r\nb", true},
		{"LF-only CRLF boundary", "a\r\nb", "\n", "\ninserted\n", "a\r\ninserted\r\nb", true},
		{"LF-only CRLF deletion", "a\r\nb", "\n", "", "ab", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := planFileEdits(tt.content, []fileEdit{{OldString: tt.old, NewString: tt.new}})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Content != tt.want || plan.LineEndingAdjusted != tt.adjusted {
				t.Fatalf("content = %q, adjusted = %v; want %q, %v", plan.Content, plan.LineEndingAdjusted, tt.want, tt.adjusted)
			}
		})
	}
}

func TestPlanFileEditsReplaceAllAndAdjacent(t *testing.T) {
	plan, err := planFileEdits("head\nx x\nend\n", []fileEdit{{OldString: "x", NewString: "X", ReplaceAll: true}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Content != "head\nX X\nend\n" || plan.Replacements != 2 || plan.FirstLine != 2 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	plan, err = planFileEdits("abcd", []fileEdit{{OldString: "cd", NewString: "Y"}, {OldString: "ab", NewString: "X"}})
	if err != nil || plan.Content != "XY" {
		t.Fatalf("adjacent reverse-order edits: %+v, %v", plan, err)
	}
	plan, err = planFileEdits("aaaa", []fileEdit{{OldString: "a", NewString: "b", ReplaceAll: true}})
	if err != nil || plan.Content != "bbbb" || plan.Replacements != 4 {
		t.Fatalf("adjacent replace_all: %+v, %v", plan, err)
	}
}

func TestPlanFileEditsFullLinePreview(t *testing.T) {
	tests := []struct {
		content, old, new string
		want              []string
	}{
		{"context\nprefix OLD suffix\ntail\n", "OLD", "NEW", []string{"  context", "- prefix OLD suffix", "+ prefix NEW suffix", "  tail"}},
		{"prefix old\nblock suffix\n", "old\nblock", "new", []string{"- prefix old", "- block suffix", "+ prefix new suffix"}},
		{"a\nb\nc\n", "b\n", "", []string{"- b", "+ c", "- c"}},

		{"prefix old suffix", "old", "new\nnext", []string{"- prefix old suffix", "+ prefix new", "+ next suffix", "\\ No newline at end of file"}},
		{"a\n", "\n", "", []string{"@@ -1,1 +1,1 @@", "+ a", "\\ No newline at end of file"}},
		{"a\n", "a\n", "", []string{"@@ -1,1 +0,0 @@", "- a"}},
	}
	for _, tt := range tests {

		plan, err := planFileEdits(tt.content, []fileEdit{{OldString: tt.old, NewString: tt.new}})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range tt.want {
			if !strings.Contains(plan.Diff, want) {
				t.Errorf("diff %q missing %q", plan.Diff, want)
			}
		}
	}
	plan, err := planFileEdits("a\nb", []fileEdit{{OldString: "\n", NewString: "!"}})
	if err != nil || !strings.Contains(plan.Diff, "+ a!b") {
		t.Fatalf("newline joining preview: %q, %v", plan.Diff, err)
	}
}

func TestPlanFileEditsBounds(t *testing.T) {
	t.Run("batch", func(t *testing.T) {
		edits := make([]fileEdit, 51)
		_, err := planFileEdits("x", edits)
		if err == nil || !strings.Contains(err.Error(), "50") {
			t.Fatalf("batch limit: %v", err)
		}
	})
	t.Run("final size", func(t *testing.T) {
		_, err := planFileEdits("x", []fileEdit{{OldString: "x", NewString: strings.Repeat("y", fileEditMaxBytes+1)}})
		if err == nil || !strings.Contains(err.Error(), "16 MiB") {
			t.Fatalf("size limit: %v", err)
		}
		plan, err := planFileEdits("xy", []fileEdit{{OldString: "x", NewString: strings.Repeat("z", fileEditMaxBytes)}, {OldString: "y", NewString: ""}})
		if err != nil || len(plan.Content) != fileEditMaxBytes {
			t.Fatalf("final rather than intermediate size: len=%d, %v", len(plan.Content), err)
		}
	})
	t.Run("preview bytes", func(t *testing.T) {
		plan, err := planFileEdits("☃"+strings.Repeat("x", 20000), []fileEdit{{OldString: "☃", NewString: "終"}})
		if err != nil {
			t.Fatal(err)
		}
		assertFileEditPreviewBounded(t, plan.Diff, true)
	})
	t.Run("preview lines", func(t *testing.T) {
		content := strings.Repeat("line\n", 100)
		plan, err := planFileEdits(content, []fileEdit{{OldString: content, NewString: "new\n"}})
		if err != nil {
			t.Fatal(err)
		}
		assertFileEditPreviewBounded(t, plan.Diff, true)
	})
	t.Run("preview hunks", func(t *testing.T) {
		content := strings.Repeat("target\n"+strings.Repeat("context\n", 10), 10)
		plan, err := planFileEdits(content, []fileEdit{{OldString: "target", NewString: "new", ReplaceAll: true}})
		if err != nil {
			t.Fatal(err)
		}
		assertFileEditPreviewBounded(t, plan.Diff, true)
		if strings.Count(plan.Diff, "@@ -") != 6 {
			t.Fatalf("expected 6 hunks: %s", plan.Diff)
		}
	})
	t.Run("ambiguity locations", func(t *testing.T) {
		_, err := planFileEdits(strings.Repeat("x\n", 100), []fileEdit{{OldString: "x", NewString: "y"}})
		if err == nil || !strings.Contains(err.Error(), "appears 100 times") || !strings.Contains(err.Error(), "lines 1, 2, 3, 4, 5, 6, 7, 8, …") {
			t.Fatalf("bounded locations: %v", err)
		}
	})
	t.Run("maximum batch", func(t *testing.T) {
		var content strings.Builder
		var edits []fileEdit
		for i := 0; i < 50; i++ {
			old := fmt.Sprintf("[%d]", i)
			content.WriteString(old + "\n")
			edits = append(edits, fileEdit{OldString: old, NewString: "changed"})
		}
		plan, err := planFileEdits(content.String(), edits)
		if err != nil || plan.Replacements != 50 {
			t.Fatalf("maximum batch: %+v, %v", plan, err)
		}
	})
}

func TestPlanFileEditsReplacementLimit(t *testing.T) {
	t.Run("repetitive input", func(t *testing.T) {
		plan, err := planFileEdits(strings.Repeat("x", fileEditMaxBytes), []fileEdit{{OldString: "x", NewString: "y", ReplaceAll: true}})
		if err == nil || !strings.Contains(err.Error(), "edit 1 at line 1") || !strings.Contains(err.Error(), "10000 planned replacements") || !strings.Contains(err.Error(), "split") || !strings.Contains(err.Error(), "context") {
			t.Fatalf("expected actionable replacement limit error, got %v", err)
		}
		if plan != (editPlan{}) {
			t.Fatal("replacement limit returned a partial plan")
		}
	})
	t.Run("total across edits", func(t *testing.T) {
		content := strings.Repeat("x", 6000) + strings.Repeat("y", 4001)
		plan, err := planFileEdits(content, []fileEdit{
			{OldString: "x", NewString: "X", ReplaceAll: true},
			{OldString: "y", NewString: "Y", ReplaceAll: true},
		})
		if err == nil || !strings.Contains(err.Error(), "edit 2") || !strings.Contains(err.Error(), "10000 planned replacements") {
			t.Fatalf("expected batch-wide replacement limit, got %v", err)
		}
		if plan != (editPlan{}) {
			t.Fatal("batch limit returned a partial plan")
		}
	})
	t.Run("unique edit consumes budget", func(t *testing.T) {
		_, err := planFileEdits(strings.Repeat("x", fileEditMaxReplacements)+"y", []fileEdit{
			{OldString: "x", NewString: "X", ReplaceAll: true},
			{OldString: "y", NewString: "Y"},
		})
		if err == nil || !strings.Contains(err.Error(), "edit 2") || !strings.Contains(err.Error(), "planned replacements") {
			t.Fatalf("unique replacement did not consume batch budget: %v", err)
		}
	})
	t.Run("exact limit allowed", func(t *testing.T) {
		plan, err := planFileEdits(strings.Repeat("x", 6000)+strings.Repeat("y", 4000), []fileEdit{
			{OldString: "x", NewString: "X", ReplaceAll: true},
			{OldString: "y", NewString: "Y", ReplaceAll: true},
		})
		if err != nil || plan.Replacements != fileEditMaxReplacements || plan.Content != strings.Repeat("X", 6000)+strings.Repeat("Y", 4000) {
			t.Fatalf("exact replacement limit: replacements=%d, error=%v", plan.Replacements, err)
		}
	})
	t.Run("ambiguous query retains occurrence count", func(t *testing.T) {
		_, err := planFileEdits(strings.Repeat("x", fileEditMaxReplacements+1), []fileEdit{{OldString: "x", NewString: "y"}})
		if err == nil || !strings.Contains(err.Error(), "appears 10001 times") {
			t.Fatalf("expected ambiguity rather than replacement limit: %v", err)
		}
	})
}

func assertFileEditPreviewBounded(t *testing.T, diff string, truncated bool) {
	t.Helper()
	if len(diff) > fileEditDiffBytes || strings.Count(diff, "\n")+1 > fileEditDiffLines || !utf8.ValidString(diff) {
		t.Fatalf("invalid preview bounds: bytes=%d lines=%d", len(diff), strings.Count(diff, "\n")+1)
	}
	if truncated && !strings.Contains(diff, "preview truncated") {
		t.Fatalf("missing truncation notice: %q", diff)
	}
}
