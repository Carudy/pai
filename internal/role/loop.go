// Package role runs PAI's single agent loop against a data-defined role.
//
// There is one loop; roles differ only by data (see internal/prompts). Adding a
// role means adding a TOML file, not Go code.
package role

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

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

	// Sessions backs the in-session storage commands (/rename, /new). nil when no
	// store is configured.
	Sessions core.Sessions
	// SessionName is the conversation's initial storage name ("" = ephemeral).
	SessionName string

	// transcript is every turn this run has recorded, kept even when nothing is
	// persisted so that naming the conversation later can backfill it.
	transcript []core.Turn

	// promptTokens is the size of the last request as reported by the provider (or
	// an estimate before the first call). It drives LLM summarization.
	promptTokens int

	mu         sync.Mutex
	cancelStep context.CancelFunc
}

func (rt *Runtime) chatPorts() chat.Ports {
	return chat.Ports{Provider: rt.Provider, Observer: rt.Observer, Logger: rt.Logger}
}

// Interrupt cancels the in-flight step, reporting whether one was running. Used
// by the signal handler so Ctrl+C stops the current work rather than the run.
func (rt *Runtime) Interrupt() bool {
	rt.mu.Lock()
	cancel := rt.cancelStep
	rt.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// beginStep returns a cancellable context for one step plus its cleanup.
func (rt *Runtime) beginStep(ctx context.Context) (context.Context, func()) {
	stepCtx, cancel := context.WithCancel(ctx)
	rt.mu.Lock()
	rt.cancelStep = cancel
	rt.mu.Unlock()
	return stepCtx, func() {
		rt.mu.Lock()
		rt.cancelStep = nil
		rt.mu.Unlock()
		cancel()
	}
}

// record appends a turn to the run's transcript and, when the conversation is
// bound to a session, persists it. The transcript is kept either way so a
// conversation can be named later. A persistence failure is reported but does
// not abort the run.
func (rt *Runtime) record(t core.Turn) {
	rt.transcript = append(rt.transcript, t)
	if rt.Recorder == nil {
		return
	}
	if err := rt.Recorder.AppendTurn(t); err != nil {
		rt.Logger.Errorf("failed to record turn: %v\n", err)
	}
}

// endsWithUser reports whether the transcript's last turn is the user's, i.e.
// the model still owes a reply.
func (rt *Runtime) endsWithUser() bool {
	n := len(rt.transcript)
	return n > 0 && rt.transcript[n-1].Role == "user"
}

// closeTurn makes the transcript end with an assistant turn, so a run that stops
// part-way (error, Ctrl+C) does not leave two consecutive user turns for the
// next attach to trip over.
func (rt *Runtime) closeTurn(err error) {
	if !rt.endsWithUser() {
		return
	}
	note := "interrupted"
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, core.ErrAborted) {
		note = "error: " + err.Error()
	}
	rt.record(core.Turn{Role: "assistant", Kind: core.KindNote, Content: "[" + note + "]"})
}

// readInstruction prompts for the next line of input. ok=false means the session
// should end (empty input, or the user cancelled the prompt). Recording and
// echoing are the caller's job: a line may turn out to be a command.
func (rt *Runtime) readInstruction() (string, bool) {
	rt.Observer.Awaiting()
	input, err := rt.Prompter.Ask("Input:")
	if err != nil {
		if !errors.Is(err, core.ErrAborted) {
			rt.Logger.Errorf("input error: %v\n", err)
		}
		return "", false
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return "", false
	}
	return input, true
}

// nextInstruction reads until the user supplies something for the model, running
// any in-session commands along the way. ok=false ends the session.
func (rt *Runtime) nextInstruction(cc *cmdCtx) (string, bool) {
	for {
		line, ok := rt.readInstruction()
		if !ok {
			return "", false
		}
		switch outcome, msg := dispatch(cc, line); outcome {
		case cmdExit:
			return "", false
		case cmdHandled:
			continue
		default:
			// Only real conversation is echoed and recorded.
			rt.Observer.User(msg)
			rt.record(core.Turn{Role: "user", Kind: core.KindInput, Content: msg})
			return msg, true
		}
	}
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

	// Repository content has entered the system prompt; say so rather than
	// letting it influence the model invisibly.
	if rp.ContextSource != "" {
		rt.Observer.Output("project instructions: " + rp.ContextSource)
	}

	err = loop(ctx, cfg, rt, rp, history, userInput)
	rt.closeTurn(err)
	return err
}

func loop(ctx context.Context, cfg *config.UserConfig, rt *Runtime, rp *chat.RolePrompt, resumed []provider.Message, userInput string) error {
	rt.Logger.Debugf("Role: %s\nIntro:\n%s\n", rp.Name, rp.Intro)

	// cc owns the conversation so that in-session commands can replace the role
	// prompt (/role) or clear the history (/new). history holds only conversation
	// turns; the role prompt is composed around it on every request.
	cc := &cmdCtx{rt: rt, cfg: cfg, rp: rp, session: rt.SessionName, ctx: ctx}
	cc.history = make([]provider.Message, 0, len(resumed)+1)
	cc.history = append(cc.history, resumed...)

	// The instruction given on the command line may itself be a command.
	if userInput != "" {
		switch outcome, msg := dispatch(cc, userInput); outcome {
		case cmdExit:
			return nil
		case cmdHandled:
			if !rt.Interactive {
				return nil
			}
			userInput = ""
		default:
			userInput = msg
		}
	}
	if userInput != "" {
		cc.history = append(cc.history, provider.Message{Role: provider.RoleUser, Content: userInput, Kind: core.KindInput})
		rt.record(core.Turn{Role: "user", Kind: core.KindInput, Content: userInput})
	}

	// Only call the model when there is something for it to answer. A fresh
	// session, or one resumed on an assistant turn (a finished "done"/
	// "terminate"), must wait for the user first: otherwise attaching re-runs the
	// last step and burns a model call on a redundant reply. History that ends on
	// a user turn (a tool result from an interrupted step, say) still continues.
	if rt.Interactive && !historyEndsWithUser(cc.history) {
		input, ok := rt.nextInstruction(cc)
		if !ok {
			return nil
		}
		cc.history = append(cc.history, provider.Message{Role: provider.RoleUser, Content: input, Kind: core.KindInput})
	}

	// Seed the budget check for a resumed session, before any real usage is known.
	rt.promptTokens = chat.EstimateTokens(cc.history)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Second compression layer: once the prompt outgrows the budget, replace
		// the oldest turns with a model-written summary. The next step reports
		// fresh usage, so at most one attempt happens per iteration.
		if chat.ShouldSummarize(cfg.Context, rt.promptTokens) {
			if ok, err := compactHistory(ctx, rt, cfg, cc); err != nil {
				rt.Logger.Errorf("compacting conversation: %v\n", err)
			} else if ok {
				rt.Observer.Output("compacted older turns into a summary to stay within the context window")
			}
			rt.promptTokens = 0
		}

		// Each step gets its own context so Ctrl+C can cancel just this step.
		stepCtx, endStep := rt.beginStep(ctx)
		next, newHistory, err := step(stepCtx, cfg, rt, cc.rp, cc.history)
		interrupted := stepCtx.Err() != nil
		endStep()

		switch {
		case interrupted:
			rt.Logger.Debugf("[Step interrupted by user]\n")
			rt.record(core.Turn{Role: "assistant", Kind: core.KindNote, Content: "[interrupted by user]"})
			if !rt.Interactive {
				return context.Canceled
			}

		case err != nil:
			return err

		case next:
			cc.history = newHistory
			rt.Observer.Separator()
			continue

		case !rt.Interactive:
			return nil
		}

		// done/terminate, or an interrupt: wait for the next instruction.
		input, ok := rt.nextInstruction(cc)
		if !ok {
			return nil
		}
		cc.history = append(cc.history, provider.Message{Role: provider.RoleUser, Content: input, Kind: core.KindInput})
		rt.Observer.Separator()
	}
}

// compactHistory replaces the oldest turns with a model-written summary, keeping
// the last Context.KeepTurns messages verbatim. It mutates cc.history; the
// recorded transcript and any stored session are untouched. It reports whether
// anything was compacted.
func compactHistory(ctx context.Context, rt *Runtime, cfg *config.UserConfig, cc *cmdCtx) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	span, recent := chat.SplitForSummary(cc.history, cfg.Context.KeepTurns)
	if len(span) == 0 {
		return false, nil
	}

	// Summarization is its own step, so Ctrl+C can cancel it like any other work.
	stepCtx, end := rt.beginStep(ctx)
	defer end()

	summary, err := chat.Summarize(stepCtx, cfg, rt.chatPorts(), chat.RenderTranscript(span))
	if err != nil {
		return false, err
	}

	msg := chat.SummaryMessage(summary)
	cc.history = append([]provider.Message{msg}, recent...)

	// Persist a checkpoint after the turns it stands in for. On attach the replay
	// starts at the last summary, so summarized turns are not replayed (and not
	// re-summarized) — while the full transcript is still kept on disk.
	rt.record(core.Turn{Role: provider.RoleSystem, Kind: core.KindSummary, Content: msg.Content})
	return true, nil
}

// historyEndsWithUser reports whether the composed conversation's last message
// awaits a reply from the model.
func historyEndsWithUser(history []provider.Message) bool {
	n := len(history)
	return n > 0 && history[n-1].Role == provider.RoleUser
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
		rt.promptTokens = usage.PromptTokens
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
		rt.record(core.Turn{Role: "assistant", Kind: core.KindOutput, Content: history[n-1].Content})
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
			if errors.Is(err, core.ErrAborted) {
				// User cancelled the question: end this turn and let the loop
				// decide (prompt again if interactive).
				return false, history, nil
			}
			return false, nil, fmt.Errorf("user input error: %w", err)
		}
		if answer != "" {
			rt.Observer.User(answer)
		} else {
			answer = "[user cancelled / no answer]"
		}
		answerTurn := "[user answer]\n" + answer
		rt.record(core.Turn{Role: "user", Kind: core.KindUserAnswer, Content: answerTurn})
		history = append(history, provider.Message{Role: provider.RoleUser, Content: answerTurn, Kind: core.KindUserAnswer})

	case chat.ActionTool:
		rt.Logger.Debugf("toolname: %s\n", resp.ToolName)

		observation, err := invokeTool(ctx, cfg, rt, rp, resp.ToolName, resp.Payload, resp.Reason)
		if err != nil {
			return false, nil, err
		}
		rt.record(core.Turn{Role: "user", Kind: core.KindToolResult, Content: observation})
		history = append(history, provider.Message{
			Role:    provider.RoleUser,
			Content: observation,
			Kind:    core.KindToolResult,
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
	toolName string,
	payload json.RawMessage,
	reason string) (string, error) {

	if !rp.HasTool(toolName) {
		return fmt.Sprintf("[tool error]\nTOOL: %s\nERROR: tool is not available to this role; available tools: %s",
			toolName, strings.Join(rp.ToolNames(), ", ")), nil
	}

	handler, ok := toolHandlers[toolName]
	if !ok {
		return "", fmt.Errorf("tool %q has no handler", toolName)
	}

	observation, err := handler(ctx, cfg, rt, reason, payload)
	if err != nil {
		return fmt.Sprintf("[tool error]\nTOOL: %s\nERROR: %v", toolName, err), nil
	}
	return observation, nil
}
