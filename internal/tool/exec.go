package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Carudy/pai/internal/ui"
)

// CancelledOutput is the sentinel value stored in ExecResult.Output when the
// user declines to run the command at the confirmation prompt.
const CancelledOutput = "[user cancelled execution]"

const cmdTimeout = 30 * time.Second

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

// ExecuteCommand runs the specified command using the system shell (or cmd/powershell on Windows).
// If userConfirm is true, it will prompt the user for confirmation before execution.
//
// The function returns both an ExecResult containing the command output and status,
// and an error if something went wrong with the execution process itself.
//
// The returned ExecResult will contain the command output, exit code, and timeout status.
// The ExecResult.Output will include error information for better display to users.
// The error return value should be checked to handle execution failures appropriately.
func ExecuteCommand(command string, userConfirm bool) (ExecResult, error) {
	command = trimCmd(command)

	if command == "" {
		return ExecResult{}, fmt.Errorf("command must not be empty")
	}

	var shell, shellArg string
	if runtime.GOOS == "windows" {
		// Use PowerShell by default on Windows, fall back to cmd
		if powershell, err := exec.LookPath("powershell.exe"); err == nil {
			shell = powershell
			shellArg = "-Command"
		} else {
			shell = "cmd.exe"
			shellArg = "/C"
		}
	} else {
		// Unix-like systems
		shell = os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		shellArg = "-c"
	}

	if userConfirm {
		exeRes, err := ui.GetUserSelected(
			"Execute this command?",
			[]string{"Yes", "No"},
		)
		if err != nil {
			return ExecResult{}, fmt.Errorf("user interaction error: %w", err)
		}
		if exeRes != "Yes" {
			return ExecResult{ExitCode: -1, Output: CancelledOutput}, nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, shellArg, command)
	output, err := cmd.CombinedOutput()

	result := ExecResult{Output: string(output)}

	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		result.Output = fmt.Sprintf("%s\n[Error: %v]", result.Output, err)
		return result, fmt.Errorf("execution error: %w", err)
	}

	return result, nil
}
