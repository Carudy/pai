package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// ArgvSpec is one direct process invocation. Argv is passed to the OS verbatim:
// there is no shell, so spaces, quotes, ';' and '$()' in an argument stay part of
// that argument. Dir is relative to the caller's chosen workspace or empty for
// the process cwd. Timeout <= 0 means no timeout. Env, when non-nil, overrides
// the inherited environment; values are already resolved (no "env:" references).
type ArgvSpec struct {
	Argv    []string
	Dir     string
	Timeout time.Duration
	Env     map[string]string
}

// ExecuteArgv runs a command without a shell, reusing the same process-group,
// cancellation, streaming, and result handling as ExecuteCommandAt. It is the
// executor for custom CLI tools and, later, command probes.
func ExecuteArgv(ctx context.Context, spec ArgvSpec, streamW io.Writer) (ExecResult, error) {
	if len(spec.Argv) == 0 || strings.TrimSpace(spec.Argv[0]) == "" {
		return ExecResult{}, errors.New("argv must not be empty")
	}
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return ExecResult{ExitCode: -1, Output: CancelledOutput}, err
	}

	cmd := exec.CommandContext(ctx, spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = spec.Dir
	if spec.Env != nil {
		cmd.Env = mergedEnv(spec.Env)
	}
	isolateProcessGroup(cmd)
	cmd.WaitDelay = waitDelay

	return runPrepared(ctx, cmd, streamW)
}

// runPrepared runs an already-configured command and captures its combined
// output. The caller owns the command's argv/dir/env and must have applied
// isolateProcessGroup and WaitDelay.
func runPrepared(ctx context.Context, cmd *exec.Cmd, streamW io.Writer) (ExecResult, error) {
	var output []byte
	var err error

	if streamW != nil {
		var buf bytes.Buffer
		mw := io.MultiWriter(&buf, streamW)
		cmd.Stdout = mw
		cmd.Stderr = mw
		err = cmd.Run()
		output = buf.Bytes()
	} else {
		output, err = cmd.CombinedOutput()
	}

	result := ExecResult{Output: string(output)}

	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}

	if ctx.Err() != nil {
		result.ExitCode = -1
		if result.Output == "" {
			result.Output = CancelledOutput
		}
		return result, ctx.Err()
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

// mergedEnv overlays overrides on the inherited environment. Building a map
// first makes an override win regardless of how the OS handles duplicate names.
func mergedEnv(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		if i := strings.IndexByte(entry, '='); i >= 0 {
			values[entry[:i]] = entry[i+1:]
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}
