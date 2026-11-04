package role

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

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

	// A user instruction is a real turn, not just a wake annotation: recording it
	// keeps it in the transcript and in the history the model reads on later wakes.
	// The event id is the in-process dedupe key, so a nacked/retried wake does not
	// duplicate the turn.
	delivered := make(map[int64]bool)

	for {
		wake, err := inbox.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) || (errors.Is(err, context.Canceled) && ctx.Err() != nil) {
				return nil
			}
			return fmt.Errorf("service inbox: %w", err)
		}
		recordDirectInput(rt, conv, wake.Events, delivered)

		// The activation inherits the trace of the work that caused it, so mail sent
		// now continues the same chain instead of starting a new one.
		rt.Trace, rt.Hops = wakeTrace(wake.Events)

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

// mailTurn renders one delivered message as the turn text the model reads: a
// bracketed header naming who spoke (and in which group), the arrival time, then
// the words. The time is on its own line so a reader can skip it; a zero time
// (an old or hand-built event) simply omits it.
func mailTurn(header string, at time.Time, body string) string {
	if at.IsZero() {
		return "[" + header + "]\n" + body
	}
	return "[" + header + "]\n" + at.Format("2006-01-02 15:04:05") + "\n" + body
}

// recordDirectInput turns durable messages addressed to this session into
// conversation turns. A service wake otherwise carries its input only in the
// situation projection, which is neither stored nor replayed; recording it keeps
// the transcript truthful and lets a later wake refer to what was asked. Peer
// mail is recorded under its own kind so it reads as speech without becoming an
// instruction: a colleague cannot answer the human's question or approve a tool.
// The event id dedupes a retried wake in this process, and the last-message check
// covers a turn already replayed from the store after a restart.
func recordDirectInput(rt *Runtime, conv *conversation, events []core.Event, delivered map[int64]bool) {
	for _, ev := range events {
		var text, kind string
		switch {
		case ev.Class == core.EventUser && ev.Kind == "user_input":
			text, kind = strings.TrimSpace(ev.Payload), core.KindInput
		case ev.Class == core.EventPeer && ev.Kind == "mail":
			body := strings.TrimSpace(ev.Payload)
			if body == "" {
				continue
			}
			// The header is part of the stored turn, so it survives compaction and
			// summarization: a later reading still knows who spoke and when. The
			// "not the user" guarantee does not ride on this wording — it is the
			// KindMail kind plus the situation block — so the header can stay
			// readable.
			sender := strings.TrimPrefix(ev.Source, "session:")
			header := "Mail from " + strconv.Quote(sender)
			if rt.GroupName != "" {
				header += " in group " + strconv.Quote(rt.GroupName)
			}
			text = mailTurn(header, ev.At, body)
			kind = core.KindMail
		case ev.Class == core.EventPeer && ev.Kind == "broadcast":
			body := strings.TrimSpace(ev.Payload)
			if body == "" {
				continue
			}
			header := "Broadcast from " + strconv.Quote(strings.TrimPrefix(ev.Source, "session:"))
			if rt.GroupName != "" {
				header += " in group " + strconv.Quote(rt.GroupName)
			}
			text = mailTurn(header, ev.At, body)
			kind = core.KindMail
		case ev.Class == core.EventPeer && ev.Kind == "escalation":
			body := strings.TrimSpace(ev.Payload)
			if body == "" {
				continue
			}
			// Host-generated, not a peer's opinion: it reports a board fact and does
			// not carry anyone's authority.
			text = mailTurn("Board escalation from the host", ev.At, body)
			kind = core.KindMail
		default:
			continue
		}
		if text == "" {
			continue
		}
		if ev.ID != 0 {
			if delivered[ev.ID] {
				continue
			}
			delivered[ev.ID] = true
			// A crash after recording the turn but before acking the event leaves the
			// event pending. The in-memory map is empty after a restart, so ask the
			// recorder whether this event's turn is already durable.
			if rec, ok := rt.Recorder.(interface{ HasTurnEvent(int64) (bool, error) }); ok {
				if exists, err := rec.HasTurnEvent(ev.ID); err != nil {
					if rt.Logger != nil {
						rt.Logger.Errorf("checking delivered event %d: %v\n", ev.ID, err)
					}
				} else if exists {
					continue
				}
			}
		}
		if n := len(conv.history); n > 0 {
			last := conv.history[n-1]
			if last.Role == provider.RoleUser && last.Kind == kind && last.Content == text {
				continue
			}
		}
		conv.history = append(conv.history, provider.Message{
			Role: provider.RoleUser, Kind: kind, Content: text,
		})
		rt.record(core.Turn{Role: "user", Kind: kind, Content: text, EventID: ev.ID})
	}
}

// wakeTrace picks the traced event that most likely caused this activation. A
// wake with no trace (an old row, or a chat-driven instruction) starts a new
// chain, so every activation has a trace to hand to mail.
func wakeTrace(events []core.Event) (string, int) {
	best := -1
	trace, hops := "", 0
	for i, ev := range events {
		if ev.Trace == "" {
			continue
		}
		if best < 0 || ev.EffectivePriority() > events[best].EffectivePriority() {
			best, trace, hops = i, ev.Trace, ev.Hops
		}
	}
	if trace == "" {
		return core.NewTrace(), 0
	}
	return trace, hops
}

// wakeResult describes the activation to the host. An interrupted activation is
// a yield: the session is fine, this episode just stopped. A failed one is an
// error even when the model had reached an outcome.
func wakeResult(res activationResult, cause error) core.WakeResult {
	out, status := res.outcome.String(), res.status
	// inform and yield both stop the activation, but they are different facts to
	// an operator: one published a report, the other had nothing to do.
	if res.action == string(chat.ActionInform) {
		out = res.action
	}
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
