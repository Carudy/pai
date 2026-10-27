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
	"strings"
	"sync"
	"time"

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
	// WorkingDir scopes local tools and project instructions to this run. Set it
	// before Run and do not mutate it while running. Empty preserves process cwd.
	WorkingDir string
	// SessionBirth is when the persisted session was created, pinned into the prompt
	// head so a resumed run composes the same bytes the previous one did. The zero
	// time means this run is ephemeral, and the head states no start time.
	SessionBirth time.Time
	// TokenScale converts the token estimate into the provider's own accounting,
	// learned from the session's last request (see session.Meta.TokenScale). Zero
	// means uncalibrated.
	TokenScale  float64
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

	// TrustedCmds are command names trusted for this run only, added when the user
	// answers a confirmation with "trust this session". They extend (never replace)
	// cfg.TrustedCmds. Held here, not in config: session trust is deliberately
	// ephemeral and re-confirmed after a restart.
	TrustedCmds []string

	// TrustedTools are custom tool names trusted for this run only (the tool's
	// "trust this session" choice). They extend cfg.TrustedTools.
	TrustedTools []string

	// TrustedPaths are directories trusted for this run only (the file tools' "trust
	// this session" choice). They extend cfg.TrustedPaths.
	TrustedPaths []string

	// transcript is every turn this run has recorded, kept even when nothing is
	// persisted so that naming the conversation later can backfill it.
	transcript []core.Turn

	// promptTokens is the size of the last request as reported by the provider (or
	// an estimate before the first call). It drives LLM summarization.
	promptTokens int

	// compactionNote is the last deterministic-elision notice emitted this run, so
	// the loop logs the elision footprint only when it changes rather than on every
	// request (elision is deterministic and would otherwise repeat verbatim).
	compactionNote string

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

// activate runs one bounded reason–act–observe episode. It contains the shared
// step loop: steering, compaction, one step at a time, and a separator between
// steps. It returns when the episode reaches a terminal outcome, is interrupted,
// or exceeds maxSteps (0 means unlimited, which is chat's current behaviour).
// The driver decides what to do next: wait for the user, or sleep until an event.
func activate(ctx context.Context, cfg *config.UserConfig, rt *Runtime, conv *conversation, maxSteps int) (activationResult, error) {
	for steps := 0; ; steps++ {
		if err := ctx.Err(); err != nil {
			return activationResult{}, err
		}
		if maxSteps > 0 && steps >= maxSteps {
			return activationResult{outcome: outcomeYield}, fmt.Errorf("activation exceeded %d steps", maxSteps)
		}

		// Steering: a message the user sent while PAI was working is delivered at
		// the next safe point — after the current action, before the model picks
		// the next one. Prompters that cannot steer (line mode) simply don't
		// implement core.Steerer.
		if s, ok := rt.Prompter.(core.Steerer); ok {
			if msg, ok := s.Steer(); ok {
				rt.Observer.User(msg)
				rt.record(core.Turn{Role: "user", Kind: core.KindInput, Content: msg})
				conv.history = append(conv.history, provider.Message{
					Role:    provider.RoleUser,
					Kind:    core.KindInput,
					Content: steeringMessage(msg),
				})
			}
		}

		// First compression layer: deterministic elision of old observations. It is
		// silent on the wire, so surface its footprint in the context log once per
		// change (a turn aging out of the verbatim window), not every request.
		if note := chat.DescribeCompaction(conv.history, cfg.Context); note != rt.compactionNote {
			rt.compactionNote = note
			if note != "" {
				rt.Observer.Output(note)
			}
		}

		// Second compression layer: once the prompt outgrows the budget, replace
		// the oldest turns with a model-written summary. The next step reports
		// fresh usage, so at most one attempt happens per iteration.
		if chat.ShouldSummarize(cfg.Context, rt.promptTokens) {
			if ok, err := compactHistory(ctx, rt, cfg, conv); err != nil {
				rt.Logger.Errorf("compacting conversation: %v\n", err)
			} else if ok {
				rt.Observer.Output("compacted older turns into a summary to stay within the context window")
			}
			rt.promptTokens = 0
		}

		// Each step gets its own context so Ctrl+C can cancel just this step.
		stepCtx, endStep := rt.beginStep(ctx)
		res, err := step(stepCtx, cfg, rt, conv.rp, conv.history)
		interrupted := stepCtx.Err() != nil
		endStep()

		switch {
		case interrupted:
			rt.Logger.Debugf("[Step interrupted by user]\n")
			rt.record(core.Turn{Role: "assistant", Kind: core.KindNote, Content: "[interrupted by user]"})
			// A step can finish just as the cancel arrives; keep the turn it
			// produced so an interactive session can refer to it after the wait.
			if res.history != nil {
				conv.history = res.history
			}
			return activationResult{outcome: outcomeYield, interrupted: true}, nil

		case err != nil:
			return activationResult{}, err

		case res.outcome == outcomeContinue:
			conv.history = res.history
			rt.Observer.Separator()

		default:
			// done/terminate/ask/yield: adopt the final history before handing
			// control back. It ends with the model's own reply, and a follow-up that
			// refers to it is unanswerable without it.
			if res.history != nil {
				conv.history = res.history
			}
			return activationResult{outcome: res.outcome, status: res.status, action: res.action}, nil
		}
	}
}

// compactHistory replaces the oldest turns with a model-written summary, keeping
// the last Context.KeepTurns user turns verbatim. It mutates conv.history; the
// recorded transcript and any stored session are untouched. It reports whether
// anything was compacted.
func compactHistory(ctx context.Context, rt *Runtime, cfg *config.UserConfig, conv *conversation) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	span, recent := chat.SplitForSummary(conv.history, cfg.Context.KeepTurns)
	if len(span) == 0 {
		return false, nil
	}
	// A span holding nothing but a previous summary has no new turns to fold in:
	// re-summarizing it would cost a model call and lose detail, not save context.
	if chat.UserTurns(span) == 0 {
		return false, nil
	}

	// Summarization is its own step, so Ctrl+C can cancel it like any other work.
	stepCtx, end := rt.beginStep(ctx)
	defer end()
	// The summarizer call does not stream, so tell the observer it is running: an
	// unexplained pause of several seconds reads as a hang.
	rt.Observer.Compacting()

	summary, err := chat.Summarize(stepCtx, cfg, rt.chatPorts(), chat.RenderTranscript(span))
	if err != nil {
		return false, err
	}

	msg := chat.SummaryMessage(summary)
	conv.history = append([]provider.Message{msg}, recent...)

	// Persist a checkpoint after the turns it stands in for. On attach the replay
	// starts at the last summary, so summarized turns are not replayed (and not
	// re-summarized) — while the full transcript is still kept on disk.
	rt.record(core.Turn{Role: provider.RoleSystem, Kind: core.KindSummary, Content: msg.Content})
	return true, nil
}

// steeringMessage marks a mid-task user message so the model can tell it apart
// from a tool result — which also arrives as a user-role message — and knows the
// plan changed underneath it.
func steeringMessage(msg string) string {
	return "[user steering]\n" + msg
}

// stepResult is what one step did. A continue outcome carries the extended
// history for the next step; a terminal one carries the action's payload as the
// status a driver may persist for a session view.
type stepResult struct {
	outcome outcome
	status  string
	action  string // the model's action when it names the terminal step (inform vs yield)
	history []provider.Message
}

// statusLimit bounds the status document persisted for a service view. The whole
// payload already lives in the transcript.
const statusLimit = 2 << 10

func clampStatus(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > statusLimit {
		return s[:statusLimit] + "…"
	}
	return s
}

// step runs one reason–act–observe turn. The returned outcome is terminal
// (yield/done/ask/terminate) only when the activation should stop; a tool call
// returns outcomeContinue and stays inside the same activation.
func step(
	ctx context.Context,
	cfg *config.UserConfig,
	rt *Runtime,
	rp *chat.RolePrompt,
	history []provider.Message) (stepResult, error) {

	ports := rt.chatPorts()

	content, newHistory, usage, promptEstimate, err := chat.ChatStr(ctx, cfg, rp, ports, history)
	if err != nil {
		return stepResult{outcome: outcomeTerminate}, err
	}
	history = newHistory

	rt.Logger.Debugf("[AI Output]:\n%s\n", content)

	if usage != nil {
		rt.promptTokens = usage.PromptTokens
		rt.Observer.Usage(core.Usage{
			Prompt:     usage.PromptTokens,
			Completion: usage.CompletionTokens,
			Total:      usage.Total(),
			Cached:     usage.CachedPromptTokens,
		})
		// Store the pair the estimate is calibrated from: what the provider counted for
		// this prompt, against what the estimator predicted for it. The store is shared,
		// so the panel and the next resumed run inherit the correction.
		if rec, ok := rt.Recorder.(core.UsageRecorder); ok && promptEstimate > 0 {
			if err := rec.SetUsage(usage.PromptTokens, promptEstimate); err != nil {
				rt.Logger.Errorf("recording token accounting: %v\n", err)
			}
		}
	} else {
		// A provider that reports no usage would otherwise freeze the budget at the
		// seed estimate; size the prompt we just sent instead.
		rt.promptTokens = chat.ScaleTokens(chat.EstimateTokens(rp.Messages(chat.ServedHistory(history, cfg.Context))), rt.TokenScale)
	}

	resp, history, err := chat.ParseResponseWithRetry(ctx, cfg, rp, ports, content, history)
	if err != nil {
		return stepResult{outcome: outcomeTerminate}, err
	}

	// Persist the accepted assistant turn (after any format retries). The store keeps
	// exactly what the model wrote; the over-run is cut when the turn is replayed
	// (see chat.TrimToAction), so the original stays available for inspection.
	if n := len(history); n > 0 && history[n-1].Role == provider.RoleAssistant {
		accepted := history[n-1].Content
		rt.record(core.Turn{Role: "assistant", Kind: core.KindOutput, Content: accepted})
		if note := chat.DescribeTrim(accepted); note != "" {
			rt.Observer.Output(note)
		}
	}

	// For tool/done the reason is shown alongside the action itself, so printing
	// it separately would be redundant.
	selfExplaining := map[chat.ActionType]bool{
		chat.ActionTool:   true,
		chat.ActionDone:   true,
		chat.ActionInform: true,
	}
	if resp.Reason != "" && !selfExplaining[resp.Action] {
		rt.Observer.Reason(resp.Reason)
	}

	rt.Logger.Debugf("[Action]: %s\n[Reason]: %s\n", resp.Action, resp.Reason)

	switch resp.Action {
	case chat.ActionDone:
		rt.Observer.Done(resp.GetPayload())
		return stepResult{outcome: outcomeDone, status: clampStatus(resp.GetPayload()), action: string(resp.Action), history: history}, nil

	case chat.ActionTerminate:
		rt.Observer.Terminate(resp.GetPayload())
		return stepResult{outcome: outcomeTerminate, status: clampStatus(resp.GetPayload()), action: string(resp.Action), history: history}, nil

	case chat.ActionAsk:
		// The question is shown and recorded like any other reply; control returns
		// to the user's normal input path instead of a separate blocking prompt. The
		// answer arrives as the next instruction, which the model reads in history.
		rt.Observer.Ask(resp.GetPayload())
		return stepResult{outcome: outcomeAsk, status: clampStatus(resp.GetPayload()), action: string(resp.Action), history: history}, nil

	case chat.ActionInform:
		// A report is the service's status document: the driver persists it and the
		// session view shows it until the next report.
		rt.Observer.Output(resp.GetPayload())
		return stepResult{outcome: outcomeYield, status: clampStatus(resp.GetPayload()), action: string(resp.Action), history: history}, nil

	case chat.ActionYield:
		// Nothing more to do this episode. The reason was shown above unless it is
		// empty; the optional payload carries host hints (until/on) that the
		// scheduler will interpret in a later phase.
		return stepResult{outcome: outcomeYield, status: clampStatus(resp.GetPayload()), action: string(resp.Action), history: history}, nil

	case chat.ActionTool:
		rt.Logger.Debugf("toolname: %s\n", resp.ToolName)

		observation, err := invokeTool(ctx, cfg, rt, rp, resp.ToolName, resp.Payload, resp.Reason)
		if err != nil {
			return stepResult{outcome: outcomeTerminate}, err
		}
		rt.record(core.Turn{Role: "user", Kind: core.KindToolResult, Content: observation})
		history = append(history, provider.Message{
			Role:    provider.RoleUser,
			Content: observation,
			Kind:    core.KindToolResult,
		})

	default:
		return stepResult{outcome: outcomeTerminate}, fmt.Errorf("unknown action %q", resp.Action)
	}

	return stepResult{history: history}, nil
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

	if resolved, ok := rp.Resolved.Get(toolName); ok {
		switch resolved.Kind {
		case prompts.ToolBuiltin:
			handler, ok := toolHandlers[toolName]
			if !ok {
				return "", fmt.Errorf("tool %q has no handler", toolName)
			}
			observation, err := handler(ctx, cfg, rt, reason, payload)
			if err != nil {
				return fmt.Sprintf("[tool error]\nTOOL: %s\nERROR: %v", toolName, err), nil
			}
			return observation, nil
		case prompts.ToolCLI:
			observation, err := runCustomCLI(ctx, cfg, rt, resolved, reason, payload)
			if err != nil {
				return fmt.Sprintf("[tool error]\nTOOL: %s\nERROR: %v", toolName, err), nil
			}
			return observation, nil
		case prompts.ToolHTTP:
			observation, err := runCustomHTTP(ctx, cfg, rt, resolved, reason, payload)
			if err != nil {
				return fmt.Sprintf("[tool error]\nTOOL: %s\nERROR: %v", toolName, err), nil
			}
			return observation, nil
		default:
			return fmt.Sprintf("[tool error]\nTOOL: %s\nERROR: unsupported tool kind %q", toolName, resolved.Kind), nil
		}
	}

	// Legacy/manual RolePrompt with no resolved set: built-ins only.
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
