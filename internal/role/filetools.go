package role

// Handlers own approval and observations; the file helpers own bounded reads,
// edit planning, and guarded publication without presentation dependencies.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/tool"
)

func (rt *Runtime) resolvePath(path string) string {
	if rt.WorkingDir != "" && !filepath.IsAbs(path) {
		return filepath.Join(rt.WorkingDir, path)
	}
	return path
}

const defaultReadLimit = 2000

type readPayload struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type editPayload struct {
	Path       string     `json:"path"`
	OldString  string     `json:"old_string"`
	NewString  string     `json:"new_string"`
	ReplaceAll bool       `json:"replace_all"`
	Edits      []fileEdit `json:"edits,omitempty"`
}

type writePayload struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Missing replacement/content fields must not silently become empty strings:
// an explicit empty string is a deletion/empty file, a typo is not.
func decodeFilePayload(payload json.RawMessage, target any, required ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("payload must be an object")
	}
	for _, key := range required {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("missing required field %q", key)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, err
	}
	return fields, nil
}

func runRead(ctx context.Context, cfg *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error) {
	var p readPayload
	if _, err := decodeFilePayload(payload, &p, "path"); err != nil {
		return "", fmt.Errorf("read payload: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return "", fmt.Errorf("read requires a non-empty path")
	}
	if p.Offset < 0 || p.Limit < 0 {
		return "", fmt.Errorf("read offset and limit must not be negative")
	}
	p.Path = rt.resolvePath(p.Path)
	if p.Offset == 0 {
		p.Offset = 1
	}
	if p.Limit == 0 {
		p.Limit = defaultReadLimit
	}
	p.Limit = min(p.Limit, defaultReadLimit)
	announced := false
	fail := func(err error) (string, error) {
		if !announced {
			rt.Observer.ToolCall(core.ToolCall{Name: "read", Target: p.Path, Reason: reason})
		}
		rt.Observer.ToolResult(core.ToolResult{Message: err.Error()})
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	// Pin the resolved name before approval so a symlink retarget cannot change
	// which file the user authorized. Reads may follow links; mutations do not.
	resolved, err := filepath.EvalSymlinks(p.Path)
	if err != nil {
		return fail(fmt.Errorf("read %s: %w", p.Path, err))
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fail(fmt.Errorf("read %s: %w", p.Path, err))
	}
	if !info.Mode().IsRegular() {
		return fail(fmt.Errorf("read %s: text tools require a regular file", p.Path))
	}
	trusted := tool.IsTrustedPathAt(resolved, rt.trustedPaths(cfg), rt.WorkingDir)
	rt.Observer.ToolCall(core.ToolCall{Name: "read", Target: p.Path, Detail: fmt.Sprintf("from line %d, up to %d lines", p.Offset, p.Limit), Reason: reason, Trusted: trusted})
	announced = true
	if cfg.ConfirmRead && !trusted {
		ok, err := rt.Prompter.Confirm(fmt.Sprintf("Read %s?", p.Path))
		if err != nil {
			return fail(fmt.Errorf("user interaction error: %w", err))
		}
		if !ok {
			rt.Observer.ToolResult(core.ToolResult{Skipped: true, Message: "Skipped"})
			return fmt.Sprintf("[read skipped]\nFILE: %s\nUSER DECLINED: the file was not read.", p.Path), nil
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	latest, err := filepath.EvalSymlinks(p.Path)
	if err != nil || latest != resolved {
		return fail(fmt.Errorf("read %s: path changed during approval; retry the read", p.Path))
	}
	current, err := os.Lstat(resolved)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return fail(fmt.Errorf("read %s: file changed during approval; retry the read", p.Path))
	}
	f, err := os.Open(resolved)
	if err != nil {
		return fail(fmt.Errorf("read %s: %w", p.Path, err))
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return fail(fmt.Errorf("read %s: %w", p.Path, err))
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return fail(fmt.Errorf("read %s: file changed during approval; retry the read", p.Path))
	}
	window, err := readTextWindow(ctx, f, p.Offset, p.Limit, cfg.Context.ExecLimit)
	if err != nil {
		return fail(fmt.Errorf("read %s: %w", p.Path, err))
	}
	if window.EOF && window.Scanned == 0 {
		rt.Observer.ToolResult(core.ToolResult{OK: true, Message: "read empty file"})
		return fmt.Sprintf("[read result]\nFILE: %s\n(empty file)", p.Path), nil
	}
	if window.Included == 0 && window.OversizeLine == 0 {
		return fail(fmt.Errorf("read offset %d is beyond end of file (%d lines)", p.Offset, window.Scanned))
	}
	var header strings.Builder
	fmt.Fprintf(&header, "FILE: %s\n", p.Path)
	if window.OversizeLine > 0 {
		fmt.Fprintf(&header, "Line %d exceeds the numbered-read byte budget; no partial line was returned. Use execute for a bounded, targeted extraction of that line, or increase context.exec_limit.", window.OversizeLine)
	} else {
		fmt.Fprintf(&header, "LINES: %d–%d", p.Offset, p.Offset+window.Included-1)
		if window.EOF && !window.More {
			fmt.Fprintf(&header, " of %d", window.Scanned)
		}
		if window.More {
			fmt.Fprintf(&header, "\n… file continues beyond line %d or the read budget was reached; read again with offset=%d", p.Offset+window.Included-1, p.Offset+window.Included)
		}
	}
	rt.Observer.ToolResult(core.ToolResult{OK: true, Message: fmt.Sprintf("read %s (%d lines)", p.Path, window.Included)})
	return "[read result]\n" + header.String() + "\n" + window.Body, nil
}

func runEdit(ctx context.Context, cfg *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error) {
	var p editPayload
	fields, err := decodeFilePayload(payload, &p, "path")
	if err != nil {
		return "", fmt.Errorf("edit payload: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return "", fmt.Errorf("edit requires a non-empty path")
	}
	p.Path = rt.resolvePath(p.Path)
	announced := false
	fail := func(err error) (string, error) {
		if !announced {
			rt.Observer.ToolCall(core.ToolCall{Name: "edit", Target: p.Path, Reason: reason})
		}
		rt.Observer.ToolResult(core.ToolResult{Message: err.Error()})
		return "", err
	}
	var edits []fileEdit
	if raw, ok := fields["edits"]; ok {
		for _, key := range []string{"old_string", "new_string", "replace_all"} {
			if _, ok := fields[key]; ok {
				return fail(fmt.Errorf("edit cannot mix edits with top-level %s", key))
			}
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return fail(fmt.Errorf("edit edits: %w", err))
		}
		for i, entry := range entries {
			var e fileEdit
			if _, err := decodeFilePayload(entry, &e, "old_string", "new_string"); err != nil {
				return fail(fmt.Errorf("edit %d payload: %w", i+1, err))
			}
			edits = append(edits, e)
		}
	} else {
		for _, key := range []string{"old_string", "new_string"} {
			if value, ok := fields[key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return fail(fmt.Errorf("edit missing required field %q", key))
			}
		}
		edits = []fileEdit{{OldString: p.OldString, NewString: p.NewString, ReplaceAll: p.ReplaceAll}}
	}
	snapshot, err := captureFileSnapshot(ctx, p.Path)
	if err != nil {
		return fail(fmt.Errorf("edit %s: %w", p.Path, err))
	}
	plan, err := planFileEdits(string(snapshot.Data), edits)
	if err != nil {
		return fail(fmt.Errorf("edit %s: %w", p.Path, err))
	}
	trusted := tool.IsTrustedPathAt(snapshot.Path, rt.trustedPaths(cfg), rt.WorkingDir)
	rt.Observer.ToolCall(core.ToolCall{Name: "edit", Target: p.Path, Detail: fmt.Sprintf("replace %s in %d edit(s)", countNoun(plan.Replacements, "occurrence"), len(edits)), Reason: reason, Diff: plan.Diff, Trusted: trusted})
	announced = true
	choice := core.TrustOnce
	dir := filepath.Dir(snapshot.Path)
	if !trusted {
		var err error
		choice, err = confirmPath(rt, fmt.Sprintf("Apply this edit to %s?", p.Path), dir)
		if err != nil {
			return fail(fmt.Errorf("user interaction error: %w", err))
		}
		if choice == core.TrustDeny {
			rt.Observer.ToolResult(core.ToolResult{Skipped: true, Message: "Skipped"})
			return fmt.Sprintf("[edit skipped]\nFILE: %s\nUSER DECLINED: the file was not changed.", p.Path), nil
		}
	}
	if err := snapshot.commit(ctx, []byte(plan.Content)); err != nil {
		return fail(fmt.Errorf("edit %s: %w; read the current file before retrying", p.Path, err))
	}
	grantFilePathTrust(rt, choice, dir, snapshot.parent)
	rt.Observer.ToolResult(core.ToolResult{OK: true, Message: "edited " + p.Path})
	out := fmt.Sprintf("[edit result]\nFILE: %s\nREPLACED: %s\nFIRST_CHANGED_LINE: %d", p.Path, countNoun(plan.Replacements, "occurrence"), plan.FirstLine)
	if plan.LineEndingAdjusted {
		out += "\nMATCH: CRLF/LF differences tolerated; replacement line endings follow the matched range or uniform file style."
	}
	return out, nil
}

func runWrite(ctx context.Context, cfg *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error) {
	var p writePayload
	if _, err := decodeFilePayload(payload, &p, "path", "content"); err != nil {
		return "", fmt.Errorf("write payload: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		return "", fmt.Errorf("write requires a non-empty path")
	}
	p.Path = rt.resolvePath(p.Path)
	announced := false
	fail := func(err error) (string, error) {
		if !announced {
			rt.Observer.ToolCall(core.ToolCall{Name: "write", Target: p.Path, Reason: reason})
		}
		rt.Observer.ToolResult(core.ToolResult{Message: err.Error()})
		return "", err
	}
	if !utf8.ValidString(p.Content) || strings.IndexByte(p.Content, 0) >= 0 {
		return fail(fmt.Errorf("write content must be UTF-8 text without NUL bytes"))
	}
	if len(p.Content) > maxEditableFileBytes {
		return fail(fmt.Errorf("write content exceeds 16 MiB"))
	}
	target, err := captureCreateTarget(ctx, p.Path)
	if err != nil {
		return fail(fmt.Errorf("write %s: %w", p.Path, err))
	}
	n := lineCount(p.Content)
	trusted := tool.IsTrustedPathAt(target.Path, rt.trustedPaths(cfg), rt.WorkingDir)
	rt.Observer.ToolCall(core.ToolCall{Name: "write", Target: p.Path, Detail: fmt.Sprintf("create %s", countNoun(n, "line")), Reason: reason, Diff: diffNewFile(p.Content), Trusted: trusted})
	announced = true
	choice := core.TrustOnce
	dir := filepath.Dir(target.Path)
	if !trusted {
		var err error
		choice, err = confirmPath(rt, fmt.Sprintf("Create %s?", p.Path), dir)
		if err != nil {
			return fail(fmt.Errorf("user interaction error: %w", err))
		}
		if choice == core.TrustDeny {
			rt.Observer.ToolResult(core.ToolResult{Skipped: true, Message: "Skipped"})
			return fmt.Sprintf("[write skipped]\nFILE: %s\nUSER DECLINED: no file was created.", p.Path), nil
		}
	}
	if err := target.commit(ctx, []byte(p.Content)); err != nil {
		return fail(fmt.Errorf("write %s: %w", p.Path, err))
	}
	grantFilePathTrust(rt, choice, dir, target.parent)
	rt.Observer.ToolResult(core.ToolResult{OK: true, Message: "created " + p.Path})
	return fmt.Sprintf("[write result]\nFILE: %s\nCREATED: %s", p.Path, countNoun(n, "line")), nil
}

// A rejected/stale operation must not grant trust to a substituted directory.
func grantFilePathTrust(rt *Runtime, choice core.TrustChoice, dir string, parent fileParent) {
	if choice != core.TrustSession && choice != core.TrustPersist {
		return
	}
	if err := parent.verify(context.Background()); err != nil {
		rt.Observer.Notice("file committed, but directory trust was not granted: " + err.Error())
		return
	}
	applyPathTrust(rt, choice, dir)
}

func diffNewFile(content string) string {
	var w fileEditPreviewWriter
	w.line("", "@@ new file @@")
	w.block("+ ", content)
	if w.truncated {
		w.body.WriteString(fileEditPreviewNotice)
	}
	return strings.TrimRight(w.body.String(), "\n")
}

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

func countNoun(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
