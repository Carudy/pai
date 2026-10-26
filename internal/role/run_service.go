package role

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

// maxServiceActivationSteps is a structural safety rail, not a cost budget: it
// stops a service whose tool calls never reach a terminal outcome. It is fixed
// for now; configurable step/token budgets are a later phase.
const maxServiceActivationSteps = 25

// RunService drives a resident session: wait for a wake, run one bounded
// activation, acknowledge it, repeat. The step loop is the same activate() used
// by chat, so compaction, tool dispatch, recording, and usage accounting cannot
// drift. The wake's situation is inserted as a projection after history; it is
// never recorded as a turn.
//
// P0 exposes the seam only. The scheduler, persistence, and production wiring
// arrive in the next phase; a fake inbox is enough to exercise the driver.
func RunService(ctx context.Context, cfg *config.UserConfig, rt *Runtime, rp *chat.RolePrompt, inbox core.Inbox, history []provider.Message) error {
	if inbox == nil {
		return errors.New("service inbox is nil")
	}
	if rp == nil {
		return errors.New("service role prompt is nil")
	}
	if err := checkToolCoverage(rp); err != nil {
		return err
	}

	// The service contract is chat plus inform/yield. Copy before changing it so
	// the caller's prepared prompt remains the chat projection it was.
	prepared := *rp
	prepared.SetActions(chat.ServiceActions())
	rp = &prepared

	conv := &conversation{rt: rt, cfg: cfg, rp: rp, session: rt.SessionName, ctx: ctx}
	conv.history = append([]provider.Message(nil), history...)
	rt.promptTokens = chat.ScaleTokens(chat.EstimateTokens(rp.Messages(chat.ServedHistory(conv.history, cfg.Context))), rt.TokenScale)

	for {
		wake, err := inbox.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) || (errors.Is(err, context.Canceled) && ctx.Err() != nil) {
				return nil
			}
			return fmt.Errorf("service inbox: %w", err)
		}

		// One situation per activation; the frozen head and the stored history
		// are shared across wakes.
		conv.rp = rp.WithSituation(wake.Situation)

		result, actErr := activate(ctx, cfg, rt, conv, maxServiceActivationSteps)
		if result.interrupted && ctx.Err() == nil {
			// A user abort ended this episode, not the session: acknowledge the
			// wake as a yield and keep waiting for the next one.
			actErr = nil
		}
		if ctx.Err() != nil {
			actErr = ctx.Err()
		}
		if doneErr := inbox.Done(wake, wakeResult(result, actErr)); doneErr != nil {
			return fmt.Errorf("ack service wake: %w", doneErr)
		}
		if actErr != nil {
			return actErr
		}
	}
}

// wakeResult describes the activation to the host. An interrupted activation is
// a yield: the session is fine, this episode just stopped. A failed one is an
// error even when the model had reached an outcome.
func wakeResult(res activationResult, cause error) core.WakeResult {
	out, status := res.outcome.String(), res.status
	if res.interrupted {
		out = outcomeYield.String()
	}
	if cause != nil {
		out = "error"
		if status == "" {
			status = cause.Error()
		}
	}
	return core.WakeResult{Outcome: out, Status: status, Err: cause}
}
