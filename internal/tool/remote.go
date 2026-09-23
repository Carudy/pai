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
	"strings"

	"github.com/Carudy/pai/internal/paths"
)

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
// $XDG_DATA_HOME/pai/ssh-control/ (or ~/.local/share/pai/ssh-control/) for
// control sockets (persistent across runs).
//
// A remote command's time budget is the caller's: runRemote applies
// [tool] cmd_timeout_seconds, so it is bounded the same way a local command is.
type RemoteManager struct {
	controlDir string
	// shell, when non-empty, is a remote login shell (e.g. "bash", "fish") used
	// to wrap each command as "<shell> -lc <cmd>". ssh runs the command through
	// the remote login shell non-interactively, which loads no profile and so may
	// miss the PATH/env the user expects (nix/asdf PATH lives in /etc/profile); an
	// explicit shell fixes that. A value containing a space is used as a verbatim
	// prefix. Empty = no wrapper, i.e. plain ssh behaviour.
	shell string
}

// NewRemoteManager creates the SSH control-socket directory under
// $XDG_DATA_HOME/pai/ssh-control/ (or ~/.local/share/pai/ssh-control/)
// and returns a ready-to-use manager. shell may be empty (see RemoteManager).
func NewRemoteManager(shell string) (*RemoteManager, error) {
	dataDir := paths.DataDir()
	if dataDir == "" {
		return nil, fmt.Errorf("cannot determine the data directory (no home directory or XDG_DATA_HOME)")
	}
	dir := filepath.Join(dataDir, "ssh-control")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create SSH control dir %s: %w", dir, err)
	}
	return &RemoteManager{controlDir: dir, shell: shell}, nil
}

// ExecuteRemote runs cmd on host (a Host alias from ~/.ssh/config).
// streamW behaves as in ExecuteCommand; confirmation is the caller's job.
func (rm *RemoteManager) ExecuteRemote(ctx context.Context, payload RemotePayload, streamW io.Writer) (ExecResult, error) {
	if payload.Host == "" || payload.Cmd == "" {
		return ExecResult{ExitCode: -1}, fmt.Errorf("remote: host and cmd required")
	}

	controlPath := filepath.Join(rm.controlDir, sanitizeHost(payload.Host))

	args := []string{
		"-o", "ControlMaster=auto",
		"-o", fmt.Sprintf("ControlPath=%s", controlPath),
		"-o", "ControlPersist=5m",
		payload.Host,
		rm.remoteCmd(payload.Cmd),
	}

	cmd := exec.CommandContext(ctx, "ssh", args...)
	// ssh is left to manage its own children (killing the group could take the
	// ControlMaster with it); WaitDelay still bounds the wait on its pipes.
	cmd.WaitDelay = waitDelay

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

// remoteCmd applies the configured login-shell wrapper, if any. ssh joins its
// command arguments with spaces and hands the result to the remote login shell,
// so cmd must be passed as one quoted word to survive that second parse.
//
// shell is a bare shell name ("bash", "fish") or a full prefix ("bash -lc").
// A bare name gets "-lc" appended so it sources login profiles — that is the
// point: nix, asdf and friends set PATH from /etc/profile, which a non-login
// shell never reads. A value containing a space is used verbatim, so a store
// path or extra flags can be expressed.
func (rm *RemoteManager) remoteCmd(cmd string) string {
	if rm.shell == "" {
		return cmd
	}
	prefix := rm.shell
	if !strings.Contains(prefix, " ") {
		prefix += " -lc"
	}
	return prefix + " " + shellQuote(cmd)
}

// shellQuote wraps s in single quotes, escaping embedded single quotes, so a
// shell re-parsing it sees exactly the original string.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
