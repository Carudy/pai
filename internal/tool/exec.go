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
)

// CancelledOutput is the sentinel value stored in ExecResult.Output when the
// user declines to run the command at the confirmation prompt.
const CancelledOutput = "[user cancelled execution]"

const cmdTimeout = 120 * time.Second

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
// command starts with a tool in the trusted list. An empty trusted list means
// nothing is trusted.
//
// Splitting honours quoting, so an operator inside a quoted string is data rather
// than a separator: `grep 'a|b' f` is one command, not two. A bare "&" counts as
// a separator because backgrounding runs the rest of the line too.
func IsTrusted(cmd string, trusted []string) bool {
	if len(trusted) == 0 {
		return false
	}
	segs := SplitSegments(cmd)
	if len(segs) == 0 {
		return false
	}
	for _, seg := range segs {
		w := firstWord(seg.Text)
		if w == "" {
			continue
		}
		if !matchTrusted(w, trusted) {
			return false
		}
	}
	return true
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

// Segment is one command in a shell chain.
type Segment struct {
	// Text is the command with the operator and surrounding space removed — what a
	// trust check inspects.
	Text string
	// Op is the operator that follows this command ("&&", "||", ";", "|", "&",
	// "\n"), or "" for the last one.
	Op string
	// Src is the segment exactly as written, operator included — what the UI shows,
	// so nothing is re-worded on the way to the reader.
	Src string
}

// SplitSegments splits a command at shell operators, honouring quoting and
// escaping so an operator inside a quoted string does not split it.
//
// This is the single source of truth for "what are the separate commands here":
// trust checks and the confirmation display both use it, so they cannot disagree
// about how many commands the user is approving.
func SplitSegments(cmd string) []Segment {
	var segs []Segment
	start := 0

	for i := 0; i < len(cmd); {
		if next, ok := skipQuoted(cmd, i); ok {
			i = next
			continue
		}

		op, n := operatorAt(cmd, i)
		if n == 0 {
			i++
			continue
		}
		if text := strings.TrimSpace(cmd[start:i]); text != "" {
			segs = append(segs, Segment{Text: text, Op: op, Src: strings.Trim(cmd[start:i+n], " \t\n")})
		}
		i += n
		start = i
	}
	if text := strings.TrimSpace(cmd[start:]); text != "" {
		segs = append(segs, Segment{Text: text, Src: strings.Trim(cmd[start:], " \t\n")})
	}
	return segs
}

// skipQuoted reports whether a quoted or escaped run starts at i, returning the
// index just past it. Escapes are honoured inside double quotes and outside
// quotes; inside single quotes everything up to the next quote is literal.
func skipQuoted(s string, i int) (next int, ok bool) {
	switch c := s[i]; {
	case c == '\'':
		for j := i + 1; j < len(s); j++ {
			if s[j] == '\'' {
				return j + 1, true
			}
		}
		return len(s), true
	case c == '"':
		for j := i + 1; j < len(s); j++ {
			if s[j] == '\\' {
				j++
				continue
			}
			if s[j] == '"' {
				return j + 1, true
			}
		}
		return len(s), true
	case c == '\\':
		return min(i+2, len(s)), true
	}
	return i, false
}

// operatorAt returns the shell operator at i and its length, or 0. A single "&"
// counts only as backgrounding: "&&", ">&", "&>" and "|&" are other tokens.
func operatorAt(s string, i int) (string, int) {
	rest := s[i:]
	switch {
	case strings.HasPrefix(rest, "&&"), strings.HasPrefix(rest, "||"), strings.HasPrefix(rest, "|&"):
		return rest[:2], 2
	case rest[0] == ';' || rest[0] == '|':
		return rest[:1], 1
	case rest[0] == '\n':
		return "\n", 1
	case rest[0] == '&':
		if i > 0 && (s[i-1] == '>' || s[i-1] == '&') {
			return "", 0
		}
		if i+1 < len(s) && (s[i+1] == '>' || s[i+1] == '&') {
			return "", 0
		}
		return "&", 1
	}
	return "", 0
}

// firstWord returns the first whitespace-delimited token of s.
func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexAny(s, " \t"); idx >= 0 {
		return s[:idx]
	}
	return s
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
	command = trimCmd(command)

	if command == "" {
		return ExecResult{}, fmt.Errorf("command must not be empty")
	}

	shell, shellArg := resolveShell()

	if err := ctx.Err(); err != nil {
		return ExecResult{ExitCode: -1, Output: CancelledOutput}, err
	}

	execCtx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, shell, shellArg, command)

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

	if execCtx.Err() == context.DeadlineExceeded {
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
