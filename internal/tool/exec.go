package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

// CancelledOutput is the sentinel value stored in ExecResult.Output when the
// user declines to run the command at the confirmation prompt.
const CancelledOutput = "[user cancelled execution]"

// waitDelay bounds how long Wait keeps waiting on a killed command's output
// pipes before giving up. See isolateProcessGroup.
const waitDelay = 5 * time.Second

// ExecResult contains the result of executing a command.
// It includes the command output, exit code, and whether the command timed out.
// The String() method formats the result for display.
type ExecResult struct {
	Output   string
	ExitCode int
	TimedOut bool
}

func (r ExecResult) String() string {
	if r.TimedOut {
		return fmt.Sprintf("[timed out]\n%s", r.Output)
	}
	return fmt.Sprintf("[exit %d]\n%s", r.ExitCode, r.Output)
}

// trimCmd removes surrounding quotes and whitespace from a command string.
// It handles single quotes, double quotes, and backticks.
func trimCmd(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	n := len(cmd)
	if n >= 2 {
		first, last := cmd[0], cmd[n-1]
		if (first == '"' && last == '"') ||
			(first == '\'' && last == '\'') ||
			(first == '`' && last == '`') {
			cmd = cmd[1 : n-1]
		}
	}
	return strings.TrimSpace(cmd)
}

// IsTrusted reports whether every command in a chained / piped / multi-line
// command is covered by the trusted list. An empty trusted list means nothing is
// trusted, except navigation builtins, which need no grant (see isNavigation).
//
// A segment with no name — a loop, a group, a heredoc, a command that writes a
// file or embeds a substitution — can never be trusted: the name would not
// describe all of what runs. See SplitSegments.
func IsTrusted(cmd string, trusted []string) bool {
	segs := SplitSegments(cmd)
	if len(segs) == 0 {
		return false
	}
	for _, seg := range segs {
		if !trustedName(seg, trusted) {
			return false
		}
	}
	return true
}

// navigationBuiltins are shell builtins that only change the working directory.
// They run no external code, so granting them trust is meaningless and asking
// about one is pure noise: a segment naming one is treated as already covered.
var navigationBuiltins = map[string]bool{"cd": true, "pushd": true, "popd": true}

// isNavigation reports whether a segment is a bare directory-change builtin. A
// nameless segment is never navigation, so this stays safe: a segment is only
// ever named when running the plain command is the whole story, which is why
// `cd /x && echo y > f` still asks (the write has no name) and `cd $(untrusted)`
// has no name either.
func isNavigation(seg Segment) bool {
	name := seg.Name
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return name != "" && navigationBuiltins[name]
}

// trustedName reports whether a segment is covered by name-based trust. A
// navigation builtin needs no grant; a nameless segment is never covered, so it
// always needs a fresh approval.
func trustedName(seg Segment, trusted []string) bool {
	if isNavigation(seg) {
		return true
	}
	return seg.Name != "" && matchTrusted(seg.Name, trusted)
}

// matchTrusted checks whether a single tool name appears in the trusted
// list (bare names and full paths are interchangeable).
func matchTrusted(first string, trusted []string) bool {
	base := first
	if i := strings.LastIndexByte(first, '/'); i >= 0 {
		base = first[i+1:]
	}
	for _, t := range trusted {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		tBase := t
		if i := strings.LastIndexByte(t, '/'); i >= 0 {
			tBase = t[i+1:]
		}
		if first == t || first == tBase || base == t || base == tBase {
			return true
		}
	}
	return false
}

// UntrustedSegments returns the indices of SplitSegments(cmd) that are not
// covered by trusted. The indices match SplitSegments order, so a UI that splits
// the same way can mark the right lines. An empty trusted list marks every
// segment.
func UntrustedSegments(cmd string, trusted []string) []int {
	var out []int
	for i, seg := range SplitSegments(cmd) {
		if !trustedName(seg, trusted) {
			out = append(out, i)
		}
	}
	return out
}

// UntrustedNames returns the distinct command names among the chain's untrusted
// segments, in order and deduplicated. These are the names an "add to trusted"
// prompt would offer; a nameless segment contributes none, because there is no
// name to trust, and a navigation builtin contributes none, because trusting it
// would be meaningless.
func UntrustedNames(cmd string, trusted []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, seg := range SplitSegments(cmd) {
		if seg.Name == "" || isNavigation(seg) || seen[seg.Name] || matchTrusted(seg.Name, trusted) {
			continue
		}
		seen[seg.Name] = true
		out = append(out, seg.Name)
	}
	return out
}

// Segment is one command in a shell chain, as the shell parser sees it.
type Segment struct {
	// Text is the command without its trailing operator, normalised by the shell
	// printer — what a display or a diagnostic should show.
	Text string
	// Name is the command name (argv[0]) when the segment is a plain command
	// invocation, or "" when it is a shell construct (a loop, conditional, group,
	// subshell, function definition or bare redirection) or when running the named
	// command is not the whole story (it writes a file, reads a heredoc, or embeds
	// a substitution). A nameless segment is never covered by name-based trust.
	Name string
	// Op is the operator that follows this command ("&&", "||", ";", "|", "|&",
	// "&", "\n"), or "" for the last one.
	Op string
	// Src is the segment as printed for a reader, operator included.
	Src string
}

// SplitSegments splits a command into its separate commands using the shell
// parser, so the split matches what the shell will actually run.
//
// This is the single source of truth for "what are the separate commands here":
// the trust check and the confirmation display both use it, so they cannot
// disagree about how many commands the user is approving.
//
// Parsing rather than scanning for operators removes two classes of false
// positives. A heredoc body is data belonging to its command, not a run of
// commands, so it no longer turns into segments called "line" or "EOF". And a
// compound construct (a loop, a conditional, a group) is one unit that carries
// no command name, so it cannot be waved through by trusting a keyword. Text the
// parser rejects becomes a single unnamed segment, so an unparseable command
// needs approval instead of being trusted by accident.
func SplitSegments(cmd string) []Segment {
	if strings.TrimSpace(cmd) == "" {
		return nil
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(cmd), "")
	if err != nil {
		return []Segment{{Text: strings.TrimSpace(cmd), Src: strings.TrimSpace(cmd)}}
	}

	var segs []Segment
	for i, st := range file.Stmts {
		flattenStmt(st, &segs)
		// Carry the separator that follows each statement, so the reader sees how
		// the commands are joined. Backgrounding wins over the statement's own
		// terminator, because it is what decides that the next command still runs.
		switch {
		case st.Background:
			setLastOp(segs, "&")
		case i < len(file.Stmts)-1:
			if st.Semicolon.IsValid() {
				setLastOp(segs, ";")
			} else {
				setLastOp(segs, "\n")
			}
		}
	}
	for i := range segs {
		segs[i].Src = withOperator(segs[i].Text, segs[i].Op)
	}
	return segs
}

// flattenStmt appends the leaf statements of st. A binary command ("a && b",
// "a | b") is flattened into its operands joined by their operator, so a chain is
// reported one command at a time; anything else is a single segment.
func flattenStmt(st *syntax.Stmt, out *[]Segment) {
	if b, ok := st.Cmd.(*syntax.BinaryCmd); ok {
		flattenStmt(b.X, out)
		setLastOp(*out, b.Op.String())
		flattenStmt(b.Y, out)
		return
	}
	*out = append(*out, segmentOf(st))
}

// segmentOf describes one leaf statement.
func segmentOf(st *syntax.Stmt) Segment {
	seg := Segment{Text: printStmt(st)}
	call, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 || !plainInvocation(st) {
		return seg
	}
	seg.Name = wordValue(call.Args[0])
	return seg
}

// plainInvocation reports whether a statement is one command with inert
// arguments. It is false when the command writes a file, reads a heredoc or
// here-string, or embeds a command or process substitution: then the command
// name does not describe everything that runs, so the segment must not inherit
// trust from that name. A file-descriptor duplication such as `2>&1` is inert and
// stays a plain invocation.
func plainInvocation(st *syntax.Stmt) bool {
	for _, r := range st.Redirs {
		switch r.Op {
		case syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc,
			syntax.RdrOut, syntax.AppOut, syntax.RdrClob,
			syntax.RdrAll, syntax.AppAll, syntax.RdrInOut:
			return false
		}
	}
	return !hasSubstitution(st)
}

// hasSubstitution reports whether a statement embeds a command or process
// substitution — code that runs but is not named by the command's argv[0].
func hasSubstitution(st *syntax.Stmt) bool {
	found := false
	syntax.Walk(st, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			found = true
			return false
		}
		return !found
	})
	return found
}

// printStmt renders a statement with the shell printer. A background statement is
// printed without its trailing "&", which Segment.Op carries instead.
func printStmt(st *syntax.Stmt) string {
	if st.Background {
		clone := *st
		clone.Background = false
		st = &clone
	}
	var sb strings.Builder
	// Printing a parsed statement does not fail; anything written is kept.
	_ = syntax.NewPrinter().Print(&sb, st)
	return strings.TrimSpace(sb.String())
}

// wordValue returns a word's value, or its printed form when the word contains an
// expansion and so has no fixed value. Quotes are transparent: `'ls'` and `"ls"`
// both yield `ls`, so a quoted command name is still recognised.
func wordValue(w *syntax.Word) string {
	var sb strings.Builder
	if !literalWord(w, &sb) {
		return printWord(w)
	}
	return sb.String()
}

// literalWord reports whether every part of w is literal, appending the value to
// sb when it is.
func literalWord(w *syntax.Word, sb *strings.Builder) bool {
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			sb.WriteString(p.Value)
		case *syntax.SglQuoted:
			sb.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return false
				}
				sb.WriteString(lit.Value)
			}
		default:
			return false
		}
	}
	return true
}

// printWord renders a word with the shell printer.
func printWord(w *syntax.Word) string {
	var sb strings.Builder
	_ = syntax.NewPrinter().Print(&sb, w)
	return sb.String()
}

// setLastOp records the operator that follows the most recent segment.
func setLastOp(segs []Segment, op string) {
	if len(segs) > 0 {
		segs[len(segs)-1].Op = op
	}
}

// withOperator joins a command with its following operator the way it is read.
func withOperator(text, op string) string {
	switch op {
	case "", "\n":
		return text
	case ";":
		return text + ";"
	default:
		return text + " " + op
	}
}

// Shell returns the short name of the shell used to execute commands
// (e.g. "bash", "powershell", "sh").
func Shell() string {
	name, _ := resolveShell()
	return filepath.Base(name)
}

// resolveShell returns (path, arg) for the shell that runs commands.
func resolveShell() (string, string) {
	if runtime.GOOS == "windows" {
		if ps, err := exec.LookPath("powershell.exe"); err == nil {
			return ps, "-Command"
		}
		return "cmd.exe", "/C"
	}
	for _, s := range []string{"/bin/bash", "/usr/bin/bash", "bash"} {
		if _, err := exec.LookPath(s); err == nil {
			return s, "-c"
		}
	}
	if s := os.Getenv("SHELL"); s != "" {
		return s, "-c"
	}
	return "/bin/sh", "-c"
}

// ExecuteCommand runs a command through the system shell (bash or sh on Unix,
// cmd or powershell on Windows).
//
// Confirmation is the caller's responsibility: this layer executes what it is
// given, which keeps it free of any presentation dependency.
//
// If streamW is non-nil, command output (stdout + stderr) is written to it in
// real time while still being captured for the returned ExecResult.
func ExecuteCommand(ctx context.Context, command string, streamW io.Writer) (ExecResult, error) {
	return ExecuteCommandAt(ctx, command, "", streamW)
}

// ExecuteCommandAt executes in workingDir using cmd.Dir, never changing process cwd.
// Empty inherits the process cwd, as ExecuteCommand does.
func ExecuteCommandAt(ctx context.Context, command, workingDir string, streamW io.Writer) (ExecResult, error) {
	command = trimCmd(command)

	if command == "" {
		return ExecResult{}, fmt.Errorf("command must not be empty")
	}

	shell, shellArg := resolveShell()

	if err := ctx.Err(); err != nil {
		return ExecResult{ExitCode: -1, Output: CancelledOutput}, err
	}

	cmd := exec.CommandContext(ctx, shell, shellArg, command)
	cmd.Dir = workingDir

	// Run the command in its own process group so cancellation kills all of it.
	// Killing only the direct child (the shell) leaves its descendants — build
	// tools, daemons — alive holding the output pipe, and Wait then blocks until
	// they exit on their own, wedging the whole agent loop.
	isolateProcessGroup(cmd)
	// Belt and braces: if a descendant escapes the group, stop waiting on the
	// pipes once cancellation has fired rather than blocking forever.
	cmd.WaitDelay = waitDelay

	var output []byte
	var err error

	if streamW != nil {
		// Streaming mode: capture into buffer while also writing to streamW.
		var buf bytes.Buffer
		mw := io.MultiWriter(&buf, streamW)
		cmd.Stdout = mw
		cmd.Stderr = mw
		err = cmd.Run()
		output = buf.Bytes()
	} else {
		// Blocking mode (original behaviour).
		output, err = cmd.CombinedOutput()
	}

	result := ExecResult{Output: string(output)}

	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}

	// Check for cancellation (e.g. SIGINT).
	if ctx.Err() != nil {
		result.ExitCode = -1
		if result.Output == "" {
			result.Output = CancelledOutput
		}
		return result, ctx.Err()
	}

	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		result.Output = fmt.Sprintf("%s\n[Error: %v]", result.Output, err)
		return result, fmt.Errorf("execution error: %w", err)
	}

	return result, nil
}
