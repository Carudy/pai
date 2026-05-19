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
	"time"

	"github.com/Carudy/pai/internal/ui"
)

const remoteTimeout = 120 * time.Second

// RemotePayload is the JSON structure the AI sends inside the "remote" action payload.
type RemotePayload struct {
	Host string `json:"host"`
	Cmd  string `json:"cmd"`
}

// RemoteManager executes commands on remote hosts via the system's ssh(1).
// Connection caching is handled by OpenSSH ControlMaster — a master connection
// is kept alive for 5 minutes after the last command, so repeated remote
// actions on the same host skip re-authentication.
//
// The manager is lazily created on the first "remote" action and uses
// ~/.config/pai/ssh-control/ for control sockets (persistent across runs).
type RemoteManager struct {
	controlDir string
}

// NewRemoteManager creates the SSH control-socket directory under
// ~/.config/pai/ssh-control/ and returns a ready-to-use manager.
func NewRemoteManager() (*RemoteManager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, ".config", "pai", "ssh-control")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create SSH control dir %s: %w", dir, err)
	}
	return &RemoteManager{controlDir: dir}, nil
}

// ExecuteRemote runs cmd on host (a Host alias from ~/.ssh/config).
// userConfirm / streamW work the same as ExecuteCommand.
func (rm *RemoteManager) ExecuteRemote(ctx context.Context, payload RemotePayload, userConfirm bool, streamW io.Writer) (ExecResult, error) {
	if payload.Host == "" || payload.Cmd == "" {
		return ExecResult{ExitCode: -1}, fmt.Errorf("remote: host and cmd required")
	}

	if userConfirm {
		exeRes, err := ui.GetUserSelected(
			fmt.Sprintf("Run on %s?", payload.Host),
			[]string{"Yes", "No"},
		)
		if err != nil {
			return ExecResult{}, fmt.Errorf("user interaction error: %w", err)
		}
		if exeRes != "Yes" {
			return ExecResult{ExitCode: -1, Output: CancelledOutput}, nil
		}
	}

	controlPath := filepath.Join(rm.controlDir, sanitizeHost(payload.Host))

	args := []string{
		"-o", "ControlMaster=auto",
		"-o", fmt.Sprintf("ControlPath=%s", controlPath),
		"-o", "ControlPersist=5m",
		payload.Host,
		payload.Cmd,
	}

	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ssh", args...)

	var result ExecResult
	var runErr error

	if streamW != nil {
		var buf bytes.Buffer
		mw := io.MultiWriter(&buf, streamW)
		cmd.Stdout = mw
		cmd.Stderr = mw
		runErr = cmd.Run()
		result = ExecResult{Output: buf.String()}
	} else {
		var out []byte
		out, runErr = cmd.CombinedOutput()
		result = ExecResult{Output: string(out)}
	}

	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}

	if runErr != nil {
		if exitErr := new(exec.ExitError); errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		result.ExitCode = -1
		result.Output += fmt.Sprintf("\n[SSH Error: %v]", runErr)
		return result, fmt.Errorf("ssh: %w", runErr)
	}

	return result, nil
}

// sanitizeHost replaces characters that are unsafe in a file path.
func sanitizeHost(h string) string {
	b := make([]byte, 0, len(h))
	for i := 0; i < len(h); i++ {
		c := h[i]
		if c == '/' || c == '\\' || c == ':' || c == '*' || c == '?' || c == ' ' {
			c = '_'
		}
		b = append(b, c)
	}
	return string(b)
}
