package role

// The file tools (read, edit) keep their I/O here rather than in internal/tool:
// each one interleaves reading, building a diff preview and confirming the
// change with the user, and confirming is explicitly the handler's job. Splitting
// those steps across packages would add plumbing without a real boundary.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/tool"
)

// defaultReadLimit bounds a single read when the model gives no limit. It is
// generous enough to take in a whole source file, while still stopping a
// runaway "read the 500k-line log" before it reaches the model.
const defaultReadLimit = 2000

// readPayload is the shape of the "read" tool's payload.
type readPayload struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"` // 1-based first line
	Limit  int    `json:"limit"`  // max lines
}

// editPayload is the shape of the "edit" tool's payload.
type editPayload struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// writePayload is the shape of the "write" tool's payload.
type writePayload struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// runRead returns a numbered window of a text file. Unlike a shell command it is
// never confirmed — reading changes nothing — and unlike `cat` it is bounded and
// line-numbered, so the model can quote an exact string for runEdit.
func runRead(ctx context.Context, cfg *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error) {
	var p readPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", fmt.Errorf("read payload: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return "", fmt.Errorf("read requires a non-empty path")
	}
	if p.Offset < 1 {
		p.Offset = 1
	}
	if p.Limit <= 0 {
		p.Limit = defaultReadLimit
	}

	// Announce the attempt before opening, so a failure is visible to the user and
	// not just an observation the model sees.
	trusted := tool.IsTrustedPath(p.Path, cfg.TrustedPaths)
	rt.Observer.ToolCall(core.ToolCall{
		Name:    "read",
		Target:  p.Path,
		Detail:  fmt.Sprintf("lines %d–%d", p.Offset, p.Offset+p.Limit-1),
		Reason:  reason,
		Trusted: trusted,
	})
	fail := func(err error) (string, error) {
		rt.Observer.ToolResult(core.ToolResult{Message: err.Error()})
		return "", err
	}

	// ConfirmRead is off by default: a read is non-destructive. When it is on, a
	// file outside the trusted paths needs approval before its contents reach the
	// model.
	if cfg.ConfirmRead && !trusted {
		ok, err := rt.Prompter.Confirm(fmt.Sprintf("Read %s?", p.Path))
		if err != nil {
			return "", fmt.Errorf("user interaction error: %w", err)
		}
		if !ok {
			rt.Observer.ToolResult(core.ToolResult{Skipped: true, Message: "Skipped"})
			return fmt.Sprintf("[read skipped]\nFILE: %s\nUSER DECLINED: the file was not read.", p.Path), nil
		}
	}

	f, err := os.Open(p.Path)
	if err != nil {
		return fail(fmt.Errorf("read %s: %w", p.Path, err))
	}
	defer f.Close()

	// Refuse binary files on a sniff of the head. Feeding NUL bytes to the model
	// is both useless and a good way to corrupt the conversation.
	br := bufio.NewReaderSize(f, 64*1024)
	if head, _ := br.Peek(8192); bytes.IndexByte(head, 0) >= 0 {
		return fail(fmt.Errorf("%s looks like a binary file; read handles text only", p.Path))
	}

	var body strings.Builder
	lineNo, included := 0, 0
	more := false
	for {
		line, rerr := br.ReadString('\n')
		if line != "" {
			lineNo++
			switch {
			case lineNo < p.Offset:
				// before the window
			case included < p.Limit:
				fmt.Fprintf(&body, "%6d\t%s\n", lineNo, trimEOL(line))
				included++
			default:
				more = true
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				return fail(fmt.Errorf("read %s: %w", p.Path, rerr))
			}
			break
		}
		if more {
			break
		}
	}

	if lineNo == 0 {
		return fmt.Sprintf("[read result]\nFILE: %s\n(empty file)", p.Path), nil
	}

	// Backstop for files with very long lines: the line count bounds the number
	// of lines, the byte budget bounds their size.
	out := chat.Truncate{MaxBytes: cfg.Context.ExecLimit}.Apply(body.String())
	header := fmt.Sprintf("FILE: %s\nLINES: %d–%d of %d", p.Path, p.Offset, p.Offset+included-1, lineNo)
	if more {
		header += fmt.Sprintf("\n… file continues beyond line %d; read again with offset=%d", lineNo-1, p.Offset+included)
	}
	rt.Observer.ToolResult(core.ToolResult{OK: true, Message: fmt.Sprintf("read %s (%d lines)", p.Path, included)})
	return "[read result]\n" + header + "\n" + out, nil
}

// trimEOL removes a trailing newline (and a preceding CR) that bufio kept.
func trimEOL(s string) string {
	s = strings.TrimSuffix(s, "\n")
	return strings.TrimSuffix(s, "\r")
}

// runEdit replaces an exact block of text in a file, showing the user a diff and
// asking before writing. Exact-match-with-unique-required is deliberate: it turns
// an ambiguous or stale edit into a loud error the model can fix, rather than a
// silent wrong change. The user still reviews the diff, but the confirmation is a
// final gate, not the only safeguard.
func runEdit(ctx context.Context, cfg *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error) {
	var p editPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", fmt.Errorf("edit payload: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return "", fmt.Errorf("edit requires a non-empty path")
	}
	// fail reports a refused edit to the user (not just to the model) — otherwise
	// a rejected call looks like the agent silently did nothing.
	fail := func(msg string) (string, error) {
		rt.Observer.ToolCall(core.ToolCall{Name: "edit", Target: p.Path, Reason: reason})
		rt.Observer.ToolResult(core.ToolResult{Message: msg})
		return "", errors.New(msg)
	}
	if p.OldString == "" {
		return fail("edit requires a non-empty old_string")
	}
	if p.OldString == p.NewString {
		return fail("edit old_string and new_string are identical")
	}

	info, err := os.Stat(p.Path)
	if err != nil {
		return fail(fmt.Sprintf("edit %s: %v", p.Path, err))
	}
	data, err := os.ReadFile(p.Path)
	if err != nil {
		return fail(fmt.Sprintf("edit %s: %v", p.Path, err))
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return fail(fmt.Sprintf("%s looks like a binary file; edit handles text only", p.Path))
	}
	content := string(data)

	n := strings.Count(content, p.OldString)
	switch {
	case n == 0:
		return fail(fmt.Sprintf("old_string not found in %s; read the file and copy the exact text", p.Path))
	case n > 1 && !p.ReplaceAll:
		return fail(fmt.Sprintf("old_string appears %d times in %s; include more surrounding context to make it unique, or set replace_all to change every occurrence", n, p.Path))
	}

	diff := diffEdit(content, p.OldString, p.NewString, n)
	trusted := tool.IsTrustedPath(p.Path, cfg.TrustedPaths)
	rt.Observer.ToolCall(core.ToolCall{
		Name:    "edit",
		Target:  p.Path,
		Detail:  fmt.Sprintf("replace %s", countNoun(n, "occurrence")),
		Reason:  reason,
		Diff:    diff,
		Trusted: trusted,
	})

	// A trusted path skips the prompt but not the diff above, so the change is
	// still visible; only the approval step is saved.
	if !trusted {
		ok, err := rt.Prompter.Confirm(fmt.Sprintf("Apply this edit to %s?", p.Path))
		if err != nil {
			return "", fmt.Errorf("user interaction error: %w", err)
		}
		if !ok {
			rt.Observer.ToolResult(core.ToolResult{Skipped: true, Message: "Skipped"})
			return fmt.Sprintf("[edit skipped]\nFILE: %s\nUSER DECLINED: the file was not changed.", p.Path), nil
		}
	}

	replacements := 1
	if p.ReplaceAll {
		replacements = -1
	}
	if err := writeFileAtomic(p.Path, []byte(strings.Replace(content, p.OldString, p.NewString, replacements)), info.Mode().Perm()); err != nil {
		return "", fmt.Errorf("write %s: %w", p.Path, err)
	}

	rt.Observer.ToolResult(core.ToolResult{OK: true, Message: "edited " + p.Path})
	return fmt.Sprintf("[edit result]\nFILE: %s\nREPLACED: %s", p.Path, countNoun(n, "occurrence")), nil
}

// runWrite creates a new file, showing the user its content and asking before it
// is written. It is create-only on purpose: overwriting is destructive and needs a
// real line diff to be reviewable, so changing an existing file is runEdit's job.
// The content travels as data rather than as a shell here-doc, which sidesteps the
// escaping bugs (quotes, `$`, a stray EOF) that make here-docs unreliable.
func runWrite(ctx context.Context, cfg *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error) {
	var p writePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", fmt.Errorf("write payload: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return "", fmt.Errorf("write requires a non-empty path")
	}
	// fail reports a refused write to the user (not just to the model).
	fail := func(msg string) (string, error) {
		rt.Observer.ToolCall(core.ToolCall{Name: "write", Target: p.Path, Reason: reason})
		rt.Observer.ToolResult(core.ToolResult{Message: msg})
		return "", errors.New(msg)
	}

	if _, err := os.Stat(p.Path); err == nil {
		return fail(fmt.Sprintf("%s already exists; use edit to change it, or read it first", p.Path))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(fmt.Sprintf("write %s: %v", p.Path, err))
	}
	if _, err := os.Stat(filepath.Dir(p.Path)); err != nil {
		return fail(fmt.Sprintf("write %s: parent directory does not exist", p.Path))
	}

	n := lineCount(p.Content)
	trusted := tool.IsTrustedPath(p.Path, cfg.TrustedPaths)
	rt.Observer.ToolCall(core.ToolCall{
		Name:    "write",
		Target:  p.Path,
		Detail:  fmt.Sprintf("create %s", countNoun(n, "line")),
		Reason:  reason,
		Diff:    diffNewFile(p.Content),
		Trusted: trusted,
	})

	if !trusted {
		ok, err := rt.Prompter.Confirm(fmt.Sprintf("Create %s?", p.Path))
		if err != nil {
			return "", fmt.Errorf("user interaction error: %w", err)
		}
		if !ok {
			rt.Observer.ToolResult(core.ToolResult{Skipped: true, Message: "Skipped"})
			return fmt.Sprintf("[write skipped]\nFILE: %s\nUSER DECLINED: no file was created.", p.Path), nil
		}
	}

	if err := writeFileAtomic(p.Path, []byte(p.Content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", p.Path, err)
	}

	rt.Observer.ToolResult(core.ToolResult{OK: true, Message: "created " + p.Path})
	return fmt.Sprintf("[write result]\nFILE: %s\nCREATED: %s", p.Path, countNoun(n, "line")), nil
}

// diffNewFile previews a new file as an all-additions diff. It is capped, since a
// long file would otherwise flood the transcript; the confirmation reports the
// true line count.
func diffNewFile(content string) string {
	const maxLines = 40
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // a trailing newline is not an extra empty line
	}

	var b strings.Builder
	b.WriteString("@@ new file @@")
	for i, l := range lines {
		if i == maxLines {
			fmt.Fprintf(&b, "\n… +%d more lines", len(lines)-i)
			break
		}
		b.WriteString("\n+ " + l)
	}
	return b.String()
}

// lineCount returns the number of lines in s, treating a trailing newline as the
// end of the last line rather than the start of an empty one.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// writeFileAtomic writes through a temp file in the same directory and renames
// it into place, so an interrupted write cannot leave a half-written file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".pai-edit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// diffEdit renders a compact review of an edit: each change site with a few lines
// of context, the replaced text marked "-" and the replacement "+". It works from
// the exact strings being replaced rather than diffing whole files, so it stays
// cheap on a large file and shows every site when replace_all is used.
func diffEdit(content, oldStr, newStr string, occurrences int) string {
	const context = 2
	const maxHunks = 6

	lines := strings.Split(content, "\n")
	oldLines := strings.Split(oldStr, "\n")
	newLines := strings.Split(newStr, "\n")

	var b strings.Builder
	shown := 0
	for i := 0; i < occurrences; i++ {
		if shown == maxHunks {
			fmt.Fprintf(&b, "\n… +%d more change(s)", occurrences-shown)
			break
		}
		idx := indexNth(content, oldStr, i)
		if idx < 0 {
			break
		}
		start := strings.Count(content[:idx], "\n") // 0-based line of the match

		from := max(start-context, 0)
		to := min(start+len(oldLines)+context, len(lines))
		after := min(start+len(oldLines), len(lines))

		fmt.Fprintf(&b, "@@ line %d @@\n", start+1)
		for _, l := range lines[from:start] {
			b.WriteString("  " + l + "\n")
		}
		for _, l := range oldLines {
			b.WriteString("- " + l + "\n")
		}
		for _, l := range newLines {
			b.WriteString("+ " + l + "\n")
		}
		for _, l := range lines[after:to] {
			b.WriteString("  " + l + "\n")
		}
		shown++
	}
	return strings.TrimRight(b.String(), "\n")
}

// indexNth returns the byte offset of the nth (0-based) occurrence of sub in s,
// or -1.
func indexNth(s, sub string, n int) int {
	off := 0
	for i := 0; i <= n; i++ {
		j := strings.Index(s[off:], sub)
		if j < 0 {
			return -1
		}
		if i == n {
			return off + j
		}
		off += j + len(sub)
	}
	return -1
}

// countNoun renders "N occurrence(s)".
func countNoun(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
