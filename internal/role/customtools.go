package role

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/tool"
)

// applyToolTrust records a custom-tool trust choice. Unlike a command grant, the
// unit is the definition name, so trusting it never trusts the executable it
// wraps.
func applyToolTrust(rt *Runtime, choice core.TrustChoice, names []string, persistent bool) {
	if (choice != core.TrustSession && choice != core.TrustPersist) || len(names) == 0 {
		return
	}
	if choice == core.TrustPersist {
		if !persistent {
			rt.Observer.Notice("project tools are trusted for this session only")
		} else if err := config.AddTrustedTools(names...); err != nil {
			rt.Observer.Notice("could not save trusted tools: " + err.Error())
		} else {
			rt.Observer.Notice("trusted from now on: " + strings.Join(names, ", "))
		}
	}
	rt.TrustedTools = append(rt.TrustedTools, names...)
}

// confirmTool asks about an untrusted custom tool, offering the trust choices
// when the prompter supports them and a plain yes/no otherwise.
func confirmTool(rt *Runtime, title string, untrusted []string, persistent, trustAll bool) (core.TrustChoice, error) {
	if len(untrusted) > 0 {
		if tc, ok := rt.Prompter.(core.ToolConfirmer); ok {
			return tc.ConfirmTool(title, untrusted, persistent, trustAll)
		}
	}
	ok, err := rt.Prompter.Confirm(title)
	if err != nil {
		return core.TrustDeny, err
	}
	if ok {
		return core.TrustOnce, nil
	}
	return core.TrustDeny, nil
}

// toolTrusted reports whether a custom tool may run without asking. The run's
// in-memory grants cover every layer; cfg.TrustedTools covers only persistent
// (user) definitions, so a project tool can never inherit a config grant.
func toolTrusted(cfg *config.UserConfig, rt *Runtime, t prompts.ResolvedTool) bool {
	if slices.Contains(rt.TrustedTools, t.Name) {
		return true
	}
	return t.Grant.Persistent && slices.Contains(cfg.TrustedTools, t.Name)
}

// authorizeTool asks about an untrusted custom tool and records the answer. It
// returns false when the user declined.
func authorizeTool(rt *Runtime, t prompts.ResolvedTool) (bool, error) {
	choice, err := confirmTool(rt, fmt.Sprintf("Run tool %s?", t.Name), []string{t.Name}, t.Grant.Persistent, t.Grant.TrustAll)
	if err != nil {
		return false, err
	}
	if choice == core.TrustDeny {
		return false, nil
	}
	applyToolTrust(rt, choice, []string{t.Name}, t.Grant.Persistent)
	return true, nil
}

// runCustomCLI executes a user-defined CLI tool. The payload is validated before
// approval, the argv is substituted without a shell, and the result is fed back
// as one observation, matching the built-in execute path.
func runCustomCLI(ctx context.Context, cfg *config.UserConfig, rt *Runtime, t prompts.ResolvedTool, reason string, payload json.RawMessage) (string, error) {
	values, err := t.ValidatePayload(payload)
	if err != nil {
		return "", err
	}
	rendered, err := t.RenderArgs(values)
	if err != nil {
		return "", err
	}
	argv := append([]string{t.CLI.Command}, rendered...)
	env, err := resolveToolEnv(t.CLI.Env)
	if err != nil {
		return "", err
	}
	dir, err := resolveExecDir(rt.WorkingDir, t.CLI.Dir)
	if err != nil {
		return "", err
	}

	timeout := time.Duration(t.CLI.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = cfg.CmdTimeout
	}
	call := core.ToolCall{
		Name:    t.Name,
		Target:  t.CLI.Command,
		Detail:  prompts.FormatArgv(argv),
		Reason:  reason,
		Trusted: toolTrusted(cfg, rt, t),
		Source:  t.Source,
	}
	// Surface only an explicitly requested directory, matching execute.
	if strings.TrimSpace(t.CLI.Dir) != "" {
		call.Dir = dir
	}
	rt.Observer.ToolCall(call)

	if !call.Trusted {
		ok, err := authorizeTool(rt, t)
		if err != nil {
			return "", fmt.Errorf("user interaction error: %w", err)
		}
		if !ok {
			output := cancelled()
			report(rt, output, nil, fmt.Sprintf("Tool %s succeeded", t.Name))
			return observation(t.Name, nil, output, execTruncate(cfg)), nil
		}
	}

	output, execErr := tool.ExecuteArgv(ctx, tool.ArgvSpec{
		Argv:    argv,
		Dir:     dir,
		Timeout: timeout,
		Env:     env,
	}, toolStream(rt))
	report(rt, output, execErr, fmt.Sprintf("Tool %s succeeded", t.Name))

	truncate := execTruncate(cfg)
	if t.OutputLimit > 0 {
		truncate.MaxBytes = t.OutputLimit
	}
	return observation(t.Name, execErr, output, truncate), nil
}

// resolveToolEnv resolves "env:NAME" references before execution. A missing
// variable is a tool error, not a crash.
func resolveToolEnv(spec map[string]string) (map[string]string, error) {
	if len(spec) == 0 {
		return nil, nil
	}
	env := make(map[string]string, len(spec))
	for key, value := range spec {
		if strings.HasPrefix(value, "env:") {
			name := strings.TrimPrefix(value, "env:")
			resolved, ok := os.LookupEnv(name)
			if !ok {
				return nil, fmt.Errorf("environment variable %q is not set", name)
			}
			env[key] = resolved
			continue
		}
		env[key] = value
	}
	return env, nil
}
