package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Carudy/pai/internal/ui"
)

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

func trimCmd(cmd string) string {
	cmd = strings.TrimSpace(cmd)

	if len(cmd) >= 2 {
		if (cmd[0] == '"' && cmd[len(cmd)-1] == '"') ||
			(cmd[0] == '\'' && cmd[len(cmd)-1] == '\'') {
			cmd = cmd[1 : len(cmd)-1]
		}
	}

	if len(cmd) >= 2 && cmd[0] == '`' && cmd[len(cmd)-1] == '`' {
		cmd = cmd[1 : len(cmd)-1]
	}

	cmd = strings.TrimSpace(cmd)
	return cmd
}

func ExecuteCommand(command string, userConfirm bool) (ExecResult, error) {
	command = trimCmd(command)

	if command == "" {
		return ExecResult{}, fmt.Errorf("command must not be empty")
	}

	var timeout = 30 * time.Second

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
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
			return ExecResult{ExitCode: -1, Output: "[user cancelled execution]"}, nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-c", command)
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
		return result, fmt.Errorf("execution error: %w", err)
	}

	result.ExitCode = 0
	return result, nil
}
