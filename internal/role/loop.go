// Package role runs PAI's single agent loop against a data-defined role.
//
// There is one loop; roles differ only by data (see internal/prompts). Adding a
// role means adding a TOML file, not Go code.
package role

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/provider"
	"github.com/Carudy/pai/internal/tool"
)

// Runtime is the host-provided state a role run needs.
//
// It is deliberately separate from config.UserConfig (configuration only):
// everything here is a port or per-run state, supplied by the caller (cli).
type Runtime struct {
	Provider    provider.Provider
	Observer    core.Observer
	Prompter    core.Prompter
	Logger      core.Logger
	Recorder    core.Recorder // nil = ephemeral (not persisted)
	Interactive bool
	Remote      *tool.RemoteManager // lazily created by the remote tool
}

// record persists a turn when this run is bound to a session. A persistence
// failure is reported but does not abort the run.
func (rt *Runtime) record(t core.Turn) {
	if rt.Recorder == nil {
		return
	}
	if err := rt.Recorder.AppendTurn(t); err != nil {
		rt.Logger.Errorf("failed to record turn: %v\n", err)
	}
}

func (rt *Runtime) chatPorts() chat.Ports {
	return chat.Ports{Provider: rt.Provider, Observer: rt.Observer, Logger: rt.Logger}
}

// Run loads the configured role and drives its reason–act–observe loop. history
// carries any turns resumed from a session.
func Run(ctx context.Context, cfg *config.UserConfig, rt *Runtime, history []provider.Message, userInput string) error {
	names := prompts.RoleNames()
	if !slices.Contains(names, cfg.DefaultRole) {
		return fmt.Errorf("unknown role %q; available roles: %s (add your own in %s)",
			cfg.DefaultRole, strings.Join(names, ", "), prompts.UserRolesDir())
	}

	rp, err := chat.LoadRolePrompt(cfg.DefaultRole, cfg.CustomPrompt)
	if err != nil {
		return fmt.Errorf("load role %q: %w", cfg.DefaultRole, err)
	}
	if err := checkToolCoverage(rp); err != nil {
		return err
	}

	return loop(ctx, cfg, rt, rp, history, userInput)
}

func loop(ctx context.Context, cfg *config.UserConfig, rt *Runtime, rp *chat.RolePrompt, resumed []provider.Message, userInput string) error {
	rt.Logger.Debugf("Role: %s\nIntro:\n%s\n", rp.Name, rp.Intro)

	// history holds only conversation turns; the role prompt is composed around
	// it on every request. It starts from any turns resumed from a session.
	history := make([]provider.Message, 0, len(resumed)+1)
	history = append(history, resumed...)
	if userInput != "" {
		history = append(history, provider.Message{Role: provider.RoleUser, Content: userInput})
		rt.record(core.Turn{Role: "user", Kind: "input", Content: userInput})
	}

	for {
		// Bail out promptly on cancellation (e.g. SIGINT).
		if err := ctx.Err(); err != nil {
			return err
		}

		next, newHistory, err := step(ctx, cfg, rt, rp, history)
		if err != nil {
			return err
		}

		if next {
			history = newHistory
		} else if rt.Interactive {
			if err := ctx.Err(); err != nil {
				return err
			}

			rt.Observer.Awaiting()
			input, err := rt.Prompter.Ask("Input:")
			if err != nil {
				return fmt.Errorf("user input error: %w", err)
			}
			if input == "" {
				return nil
			}
			rt.Observer.User(input)
			rt.record(core.Turn{Role: "user", Kind: "input", Content: input})
			history = append(history, provider.Message{Role: provider.RoleUser, Content: input})
		} else {
			return nil
		}

		rt.Observer.Separator()
	}
}

// step runs one reason–act–observe turn. It returns next=false when the loop
// should stop (done/terminate, or a non-interactive finish).
func step(
	ctx context.Context,
	cfg *config.UserConfig,
	rt *Runtime,
	rp *chat.RolePrompt,
	history []provider.Message) (bool, []provider.Message, error) {

	ports := rt.chatPorts()

	content, newHistory, usage, err := chat.ChatStr(ctx, cfg, rp, ports, history)
	if err != nil {
		return false, nil, err
	}
	history = newHistory

	rt.Logger.Debugf("[AI Output]:\n%s\n", content)

	if usage != nil {
		rt.Observer.Usage(core.Usage{
			Prompt:     usage.PromptTokens,
			Completion: usage.CompletionTokens,
			Total:      usage.Total(),
		})
	}

	resp, history, err := chat.ParseResponseWithRetry(ctx, cfg, rp, ports, content, history)
	if err != nil {
		return false, nil, err
	}

	// Persist the accepted assistant turn (after any format retries).
	if n := len(history); n > 0 && history[n-1].Role == provider.RoleAssistant {
		rt.record(core.Turn{Role: "assistant", Kind: "output", Content: history[n-1].Content})
	}

	// For tool/done the reason is shown alongside the action itself, so printing
	// it separately would be redundant.
	selfExplaining := map[chat.ActionType]bool{
		chat.ActionTool: true,
		chat.ActionDone: true,
	}
	if resp.Reason != "" && !selfExplaining[resp.Action] {
		rt.Observer.Reason(resp.Reason)
	}

	rt.Logger.Debugf("[Action]: %s\n[Reason]: %s\n", resp.Action, resp.Reason)

	switch resp.Action {
	case chat.ActionDone:
		rt.Observer.Done(resp.GetPayload())
		return false, history, nil

	case chat.ActionTerminate:
		rt.Observer.Terminate(resp.GetPayload())
		return false, history, nil

	case chat.ActionAsk:
		rt.Observer.Ask(resp.GetPayload())

		answer, err := rt.Prompter.Ask("Your answer:")
		if err != nil {
			return false, nil, fmt.Errorf("user input error: %w", err)
		}
		if answer != "" {
			rt.Observer.User(answer)
		} else {
			answer = "[user cancelled / no answer]"
		}
		answerTurn := "[user answer]\n" + answer
		rt.record(core.Turn{Role: "user", Kind: "user_answer", Content: answerTurn})
		history = append(history, provider.Message{Role: provider.RoleUser, Content: answerTurn})

	case chat.ActionTool:
		tp, err := resp.GetToolPayload()
		if err != nil {
			return false, nil, err
		}
		rt.Logger.Debugf("toolname: %s\n", tp.ToolName)

		observation, err := invokeTool(ctx, cfg, rt, rp, tp, resp.Reason)
		if err != nil {
			return false, nil, err
		}
		rt.record(core.Turn{Role: "user", Kind: "tool_result", Content: observation})
		history = append(history, provider.Message{
			Role:    provider.RoleUser,
			Content: observation,
		})

	default:
		return false, nil, fmt.Errorf("unknown action %q", resp.Action)
	}

	return true, history, nil
}

// invokeTool dispatches a tool call and returns the observation to feed back to
// the model. Bad calls (tool not enabled for this role, malformed payload) are
// returned as observations rather than errors, so the agent can correct itself
// instead of the session aborting.
func invokeTool(
	ctx context.Context,
	cfg *config.UserConfig,
	rt *Runtime,
	rp *chat.RolePrompt,
	tp chat.ToolPayload,
	reason string) (string, error) {

	if !rp.HasTool(tp.ToolName) {
		return fmt.Sprintf("[tool error]\nTOOL: %s\nERROR: tool is not available to this role; available tools: %s",
			tp.ToolName, strings.Join(rp.ToolNames(), ", ")), nil
	}

	handler, ok := toolHandlers[tp.ToolName]
	if !ok {
		return "", fmt.Errorf("tool %q has no handler", tp.ToolName)
	}

	observation, err := handler(ctx, cfg, rt, reason, tp.Payload)
	if err != nil {
		return fmt.Sprintf("[tool error]\nTOOL: %s\nERROR: %v", tp.ToolName, err), nil
	}
	return observation, nil
}
