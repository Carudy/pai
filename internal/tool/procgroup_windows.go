//go:build windows

package tool

import "os/exec"

// isolateProcessGroup makes cancellation kill the command. Windows has no POSIX
// process groups, so this kills only the direct child; cmd.WaitDelay (set by the
// caller) still bounds the wait on its output pipes, so a stray descendant
// cannot wedge the loop the way it can on Unix.
func isolateProcessGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
