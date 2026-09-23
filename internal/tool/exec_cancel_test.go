package tool

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// A command that spawns a background descendant used to wedge the caller: the
// context was cancelled, but cancelling killed only the shell, the descendant
// kept the output pipe open, and cmd.Wait blocked until it exited on its own.
func TestExecuteCommandCancelKillsDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell semantics")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	done := make(chan struct{})
	var err error
	go func() {
		_, err = ExecuteCommand(ctx, "sleep 30 & sleep 30", nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ExecuteCommand did not return after cancellation (a descendant kept the output pipe open)")
	}
	if err == nil {
		t.Error("expected a cancellation error")
	}
}

// A caller-set deadline is reported as a timeout, not as an error, and it must
// still return promptly because the process group is killed.
func TestExecuteCommandTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell semantics")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan ExecResult, 1)
	go func() {
		res, _ := ExecuteCommand(ctx, "sleep 30", nil)
		done <- res
	}()

	select {
	case res := <-done:
		if !res.TimedOut {
			t.Errorf("TimedOut = false, result = %+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ExecuteCommand did not return at its deadline")
	}
}
