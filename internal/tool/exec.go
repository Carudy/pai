package tool

import (
	"io"
	"os"
	"os/exec"
)

func ExecuteCommand(stdout io.Writer, command string) (string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell, "-c", command)
	output, err := cmd.CombinedOutput()

	return string(output), err
}
