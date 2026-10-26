package role

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/provider"
)

// The chat driver. It owns the input/wait policy around the shared activation
// loop (see loop.go); service sessions use run_service.go instead.

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
func (rt *Runtime) nextInstruction(conv *conversation) (string, bool) {
	for {
		line, ok := rt.readInstruction()
		if !ok {
			return "", false
		}
		switch outcome, msg := dispatch(conv, line); outcome {
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

	if rt.WorkingDir != "" {
		wd, err := filepath.Abs(rt.WorkingDir)
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}
		rt.WorkingDir = wd
	}
	rp, err := chat.LoadRolePromptForSession(cfg.DefaultRole, cfg.RoleExtra, cfg.RoleExtraTools, cfg.TrustedProjects, rt.WorkingDir, rt.SessionBirth)
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

	// The elision trigger is stated in provider tokens, but an estimate is what decides
	// it; convert it once, for the whole run, so a calibrated session trims at the size
	// it actually asked for. cfg is shared, so scale a copy.
	if scaled := chat.ScaledContext(cfg.Context, rt.TokenScale); scaled != cfg.Context {
		copyCfg := *cfg
		copyCfg.Context = scaled
		cfg = &copyCfg
	}

	err = runChat(ctx, cfg, rt, rp, history, userInput)
	rt.closeTurn(err)
	return err
}

// runChat is the interactive waiting policy around activate: run one episode,
// then wait for the next instruction. It is not a second loop; the steps inside
// the episode are the shared activate/step path.
func runChat(ctx context.Context, cfg *config.UserConfig, rt *Runtime, rp *chat.RolePrompt, resumed []provider.Message, userInput string) error {
	rt.Logger.Debugf("Role: %s\nIntro:\n%s\n", rp.Name, rp.Intro)

	// One value owns the conversation so that in-session commands can replace the
	// role prompt (/role) or clear the history (/new). history holds only
	// conversation turns; the role prompt is composed around it on every request.
	conv := &conversation{rt: rt, cfg: cfg, rp: rp, session: rt.SessionName, ctx: ctx}
	conv.history = make([]provider.Message, 0, len(resumed)+1)
	conv.history = append(conv.history, resumed...)

	// The instruction given on the command line may itself be a command.
	if userInput != "" {
		switch outcome, msg := dispatch(conv, userInput); outcome {
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
		conv.history = append(conv.history, provider.Message{Role: provider.RoleUser, Content: userInput, Kind: core.KindInput})
		rt.record(core.Turn{Role: "user", Kind: core.KindInput, Content: userInput})
	}

	// Only call the model when there is something for it to answer. A fresh
	// session, or one resumed on an assistant turn (a finished "done"/
	// "terminate"), must wait for the user first: otherwise attaching re-runs the
	// last step and burns a model call on a redundant reply. History that ends on
	// a user turn (a tool result from an interrupted step, say) still continues.
	if rt.Interactive && !conv.endsWithUser() {
		input, ok := rt.nextInstruction(conv)
		if !ok {
			return nil
		}
		conv.history = append(conv.history, provider.Message{Role: provider.RoleUser, Content: input, Kind: core.KindInput})
	}

	// Seed the budget check for a resumed session, before any real usage is known.
	// Size the composed prompt (system head and tail included) as it will actually be
	// sent — the over-run cut and elision included — so the budget starts from the
	// request rather than from text that will not leave the machine. The scale turns the
	// estimate into the provider's accounting, which is the unit the budget is stated in.
	rt.promptTokens = chat.ScaleTokens(chat.EstimateTokens(conv.rp.Messages(chat.ServedHistory(conv.history, cfg.Context))), rt.TokenScale)

	for {
		result, err := activate(ctx, cfg, rt, conv, 0)
		switch {
		case err != nil:
			return err
		case result.interrupted && !rt.Interactive:
			return context.Canceled
		case !rt.Interactive:
			return nil
		}

		// done/terminate/ask, a yield, or an interactive interrupt: wait for the
		// next instruction. activate already adopted the step's final history.
		input, ok := rt.nextInstruction(conv)
		if !ok {
			return nil
		}
		conv.history = append(conv.history, provider.Message{Role: provider.RoleUser, Content: input, Kind: core.KindInput})
		rt.Observer.Separator()
	}
}
