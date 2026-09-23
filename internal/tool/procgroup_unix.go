//go:build !windows

package tool

import (
	"os/exec"
	"syscall"
)

// isolateProcessGroup puts cmd in its own process group and makes cancellation
// kill the entire group.
//
// exec.CommandContext's default behaviour kills only the direct child — the
// shell — while its descendants (compilers, package managers, spawned daemons)
// keep running and keep the command's output pipe open. exec.Cmd.Wait then waits
// for that pipe to reach EOF and blocks until they exit, which hangs the agent
// loop mid-step where no context cancellation can reach it.
func isolateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// A negative pid targets the whole process group.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
