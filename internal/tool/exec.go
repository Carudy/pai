package tool

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/Carudy/pai/internal/ui"
)

func ExecuteCommand(stdout io.Writer, command string, user_confirm bool) (string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	var output string
	var _output []byte
	var execErr error

	if user_confirm {
		exeRes, err := ui.GetUserSelected("Execute this command?", []string{"Yes", "No"})
		if err != nil {
			return "", fmt.Errorf("user interaction error: %w", err)
		}

		if exeRes != "Yes" {
			output = "[user cancelled execution]"
			return output, nil
		}
	}
	cmd := exec.Command(shell, "-c", command)
	_output, execErr = cmd.CombinedOutput()
	output = string(_output)

	return output, execErr
}
