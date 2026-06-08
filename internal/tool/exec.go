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

	"github.com/Carudy/pai/internal/ui"
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

// IsTrusted reports whether every sub-command in a potentially chained /
// piped / multi-line command starts with a tool in the trusted list.
// Delimiters recognised: && || ; | (single pipe) and newlines.
// An empty trusted list means nothing is trusted.
func IsTrusted(cmd string, trusted []string) bool {
	if len(trusted) == 0 {
		return false
	}
	segs := splitCommands(cmd)
	if len(segs) == 0 {
		return false
	}
	for _, seg := range segs {
		w := firstWord(strings.TrimSpace(seg))
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

// splitCommands breaks a command string into individual sub-commands at
// shell delimiters: &&, ||, ;, | (single pipe), and newlines.
func splitCommands(s string) []string {
	// Replace multi-char delimiters first to avoid partial matches.
	s = strings.ReplaceAll(s, "&&", "\x00")
	s = strings.ReplaceAll(s, "||", "\x00")
	s = strings.ReplaceAll(s, ";", "\x00")
	s = strings.ReplaceAll(s, "\n", "\x00")
	// Single pipe — only after || is already replaced.
	s = strings.ReplaceAll(s, "|", "\x00")

	parts := strings.Split(s, "\x00")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
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

// ExecuteCommand runs the specified command using the system shell (or cmd/powershell on Windows).
// If userConfirm is true, it will prompt the user for confirmation before execution.
//
// If streamW is non-nil, command output (stdout + stderr) is written to it in real-time
// while still being captured for the returned ExecResult. Pass os.Stdout to let the user
// see live output instead of waiting until the command finishes.
//
// The function returns both an ExecResult containing the command output and status,
// and an error if something went wrong with the execution process itself.
//
// The returned ExecResult will contain the command output, exit code, and timeout status.
// The ExecResult.Output will include error information for better display to users.
// The error return value should be checked to handle execution failures appropriately.
func ExecuteCommand(command string, userConfirm bool, streamW io.Writer) (ExecResult, error) {
	command = trimCmd(command)

	if command == "" {
		return ExecResult{}, fmt.Errorf("command must not be empty")
	}

	shell, shellArg := resolveShell()

	if userConfirm {
		ok, err := ui.GetUserConfirm("Execute this command?")
		if err != nil {
			return ExecResult{}, fmt.Errorf("user interaction error: %w", err)
		}
		if !ok {
			return ExecResult{ExitCode: -1, Output: CancelledOutput}, nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, shellArg, command)

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
