package runner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/tool"
	"github.com/Carudy/pai/internal/watcher"
)

// maxDueEvents bounds how many recovered events one activation carries. A
// backlog larger than this is delivered over several activations rather than
// building an enormous situation block.
const maxDueEvents = 32

// maxProbeBudget bounds the time one activation spends probing before it looks
// at its events. Probes are I/O and may each run for their own timeout; without
// a shared budget, a user instruction could wait minutes behind them.
const maxProbeBudget = 15 * time.Second

// probeDue runs every watcher that is due at now, persists what each observed,
// and returns the events worth waking the model for plus the earliest next run.
//
// This runs at the head of an activation: the probe decides whether the clock
// tick that woke the scheduler is worth a model call at all. A backend without
// watcher storage has nothing to probe, and the caller falls back to the
// manifest's interval.
func (m *Manager) probeDue(ctx context.Context, w *worker, now time.Time) ([]core.Event, *time.Time) {
	wb, ok := m.backend.(ServiceWatcherBackend)
	if !ok {
		return nil, nil
	}
	m.mu.Lock()
	id, dir := w.sessionID, w.workingDir
	name := w.name
	m.mu.Unlock()
	if id == "" {
		return nil, nil
	}

	watchers, err := wb.Watchers(id)
	if err != nil {
		w.emit("error", "could not read watchers: "+err.Error())
		return nil, nil
	}

	var events []core.Event
	var next *time.Time

	// One budget for the whole pass, so a slow probe cannot starve the events the
	// activation was actually woken for.
	probeCtx, cancel := context.WithTimeout(ctx, maxProbeBudget)
	defer cancel()

	for _, ws := range watchers {
		if !ws.Enabled {
			continue
		}
		spec, err := config.ParseWatcherSpec(ws.Spec)
		if err != nil {
			// Leave it unscheduled: a corrupt row is a config error the user sees
			// in last_error, not a reason to spin or to kill the service.
			_ = wb.SetWatcherState(ws.ID, ws.LastRun, nil, ws.LastState, "invalid watcher spec: "+err.Error())
			m.diag(Diagnostic{Session: name, Kind: "watcher", Subject: ws.Name, Outcome: "failed",
				Detail: "invalid spec: " + err.Error()})
			continue
		}
		if ws.NextRun != nil && ws.NextRun.After(now) {
			next = earlier(next, ws.NextRun)
			continue
		}
		if probeCtx.Err() != nil {
			// Out of budget: keep it due so the next activation retries it.
			next = earlier(next, &now)
			m.diag(Diagnostic{Session: name, Kind: "watcher", Subject: ws.Name, Outcome: "deferred",
				Detail: "probe budget exhausted; retries on the next activation"})
			continue
		}

		probeNow := now
		if spec.Command != "" && ws.ApprovedAt == nil && !m.probeTrusted(w, spec.Command) {
			// No pre-approval and no trust: ask with the same card a chat tool call
			// uses. This blocks the activation until the operator answers; Stop or
			// Cancel releases it and leaves the probe due for the next pass.
			choice, err := m.confirmProbe(w, spec)
			if err != nil {
				// Cancel or Stop aborted the prompt. Retry on the normal cadence rather
				// than keeping it due: an immediate re-ask would spin while the operator
				// has just said "not now".
				nextRun := now.Add(spec.Interval)
				if err := wb.SetWatcherState(ws.ID, ws.LastRun, &nextRun, ws.LastState, ws.LastError); err != nil {
					w.emit("error", fmt.Sprintf("could not reschedule watcher %q: %v", ws.Name, err))
				}
				next = earlier(next, &nextRun)
				m.diag(Diagnostic{Session: name, Kind: "watcher", Subject: ws.Name, Outcome: "deferred",
					Detail: "approval cancelled; retries on the next interval", Next: nextRun})
				continue
			}
			if choice == core.TrustDeny {
				nextRun := now.Add(spec.Interval)
				if err := wb.SetWatcherState(ws.ID, ws.LastRun, &nextRun, ws.LastState, ws.LastError); err != nil {
					w.emit("error", fmt.Sprintf("could not reschedule watcher %q: %v", ws.Name, err))
				}
				next = earlier(next, &nextRun)
				m.diag(Diagnostic{Session: name, Kind: "watcher", Subject: ws.Name, Outcome: "denied",
					Detail: spec.Command, Next: nextRun})
				continue
			}
			// The wait was the operator's, not the probe's: run from now so neither
			// the next run nor the episode timestamps are backdated by it.
			probeNow = time.Now()
		}

		runCtx := probeCtx
		if runCtx.Err() != nil {
			// The pass budget may have expired while waiting for the answer; an
			// approved probe deserves a fresh one instead of failing on that clock.
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(ctx, maxProbeBudget)
			defer cancel()
		}
		res := watcher.Probe(runCtx, spec, dir, ws.LastState, probeNow)
		if runCtx.Err() != nil {
			// The budget cut this probe short; that is not a probe failure.
			next = earlier(next, &now)
			m.diag(Diagnostic{Session: name, Kind: "watcher", Subject: ws.Name, Outcome: "deferred",
				Detail: "probe budget exhausted; retries on the next activation"})
			continue
		}
		nextRun := res.NextRun
		var ev core.Event
		if res.Fire {
			ev = watcherEvent(id, spec, res, now)
			if eb, ok := m.backend.(ServiceEventBackend); ok {
				eid, err := eb.AppendEvent(id, ev)
				if err != nil {
					// Do not advance the watcher state before its observation is durable. If
					// the queue write fails, the same probe stays due and retries on the next
					// activation instead of being marked as already reported and lost.
					next = earlier(next, &now)
					w.emit("error", fmt.Sprintf("could not queue watcher %q: %v", ws.Name, err))
					m.diag(Diagnostic{Session: name, Kind: "watcher", Subject: ws.Name, Outcome: "failed",
						Detail: "could not queue the event; probe remains due: " + err.Error(), Next: now})
					continue
				}
				ev.ID = eid
			}
		}
		if err := wb.SetWatcherState(ws.ID, &probeNow, &nextRun, res.State, errorText(res.Err)); err != nil {
			w.emit("error", fmt.Sprintf("could not persist watcher %q: %v", ws.Name, err))
		}
		next = earlier(next, &nextRun)
		if res.Fire {
			events = append(events, ev)
			m.diag(Diagnostic{Session: name, Kind: "watcher", Subject: ws.Name, Outcome: "fired",
				Detail: watcherDetail(spec, res), Next: nextRun})
			continue
		}
		outcome := "quiet"
		reason := ""
		switch {
		case res.Err != nil:
			outcome = "failed"
		case res.Holds:
			// The predicate is true but this pass must not wake the model: either
			// the episode was already reported, or the reminder is not due yet.
			outcome = "held"
			if spec.RepeatAfter > 0 {
				reason = "reminder not due"
			} else {
				reason = "already reported this episode"
			}
		}
		m.diag(Diagnostic{Session: name, Kind: "watcher", Subject: ws.Name, Outcome: outcome,
			Detail: watcherDetail(spec, res), Reason: reason, Next: nextRun})
	}

	return events, next
}

// watcherDetail is the one-line observation a diagnostic carries: the probe's
// raw stdout (the decision plus whatever explained it), or its failure. A timer
// has no command output, so its prompt is the only thing worth showing.
func watcherDetail(spec config.WatcherSpec, res watcher.Result) string {
	if res.Err != nil {
		return res.Err.Error()
	}
	if spec.Command == "" {
		return res.Summary
	}
	if res.Output != "" {
		return res.Output
	}
	return "no output"
}

// watcherEvent turns one fired probe into a durable event. A timer probe is a
// clock tick with a name; a command probe is a condition change. Priority is the
// probe's numeric ordering hint; it is not a policy change, so a command probe
// remains an EventWatcher.
func watcherEvent(sessionID string, spec config.WatcherSpec, res watcher.Result, now time.Time) core.Event {
	class, kind := core.EventWatcher, "watch"
	if spec.Command == "" {
		class, kind = core.EventTimer, "timer"
	}
	payload := res.Summary
	if spec.Command != "" && spec.Prompt != "" {
		// A command probe's prompt is extra guidance for acting on its output.
		if payload != "" {
			payload += "\n"
		}
		payload += "Instruction: " + spec.Prompt
	}
	return core.Event{
		Session: sessionID, Class: class, Priority: spec.Priority, Kind: kind,
		Source: "watch:" + spec.Name, Payload: payload, DedupKey: "watch:" + spec.Name, At: now,
	}
}

// dueEvents returns pending events whose retry time has come, so work survives a
// restart or a failed activation. The wake may already carry some of them; the
// caller dedupes by id. more reports that the query hit its cap, so the caller
// schedules another activation immediately instead of leaving a backlog to age.
func (m *Manager) dueEvents(w *worker, now time.Time) (events []core.Event, more bool) {
	eb, ok := m.backend.(ServiceEventBackend)
	if !ok {
		return nil, false
	}
	m.mu.Lock()
	id := w.sessionID
	m.mu.Unlock()
	if id == "" {
		return nil, false
	}
	events, err := eb.DueEvents(id, now, maxDueEvents+1)
	if err != nil {
		w.emit("error", "could not read due events: "+err.Error())
		return nil, false
	}
	if len(events) > maxDueEvents {
		events, more = events[:maxDueEvents], true
	}
	if len(events) > 0 {
		m.diag(Diagnostic{Session: w.name, Kind: "event", Outcome: "recovered",
			Detail: fmt.Sprintf("%d due event(s)", len(events))})
	}
	return events, more
}

// serviceManifest returns the parsed stored manifest, caching the raw spec. A
// nil result means the service has no usable manifest.
func (m *Manager) serviceManifest(name string) *config.ServiceManifest {
	m.mu.Lock()
	spec := m.serviceSpecs[name]
	m.mu.Unlock()
	if spec == "" {
		if b, ok := m.backend.(ServiceInfoBackend); ok {
			if stored, err := b.ServiceSpec(name); err == nil && stored != "" {
				m.mu.Lock()
				m.serviceSpecs[name] = stored
				m.mu.Unlock()
				spec = stored
			}
		}
	}
	manifest, err := config.ParseStoredServiceSpec(spec)
	if err != nil {
		return nil
	}
	return manifest
}

// openItem is work a watcher opened that the model has not closed yet.
type openItem struct {
	Name  string
	Fired time.Time
}

// probeTrusted reports whether a probe's command may run without a fresh grant.
// It is the same name-based test the shell tool uses: every segment's command
// name must be trusted, with navigation exempt. A construct or a redirection has
// no name and is therefore never trusted, so those probes need an explicit
// grant. Trust-all is the session's own switch and outranks the list.
func (m *Manager) probeTrusted(w *worker, command string) bool {
	m.mu.Lock()
	trustAll := m.trustAll[w.name]
	m.mu.Unlock()
	return trustAll || tool.IsTrusted(command, m.probeTrustedList(w))
}

// probeTrustedList is the merge of the config allowlist and the session's own
// grants, as of now: a "trust session" answer from the confirmation card must
// apply to the next probe without re-preparing the runtime.
func (m *Manager) probeTrustedList(w *worker) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	trusted := append([]string(nil), w.cfgTrusted...)
	return append(trusted, commandTrustNames(m.commandTrust[w.name])...)
}

// confirmProbe asks whether one command probe may run, using the same card and
// trust choices as a chat tool call: once runs it now, session/always record the
// grant, deny skips it. The probe pass runs on the scheduler's goroutine, so this
// holds that activation until the operator answers; Stop or Cancel releases it.
func (m *Manager) confirmProbe(w *worker, spec config.WatcherSpec) (core.TrustChoice, error) {
	names := tool.UntrustedNames(spec.Command, m.probeTrustedList(w))
	m.diag(Diagnostic{Session: w.name, Kind: "watcher", Subject: spec.Name, Outcome: "approval required",
		Detail: spec.Command})
	// The prompt card shows the tool that is about to run; a probe is the execute
	// tool driven by the scheduler instead of by the model.
	m.mu.Lock()
	w.latestTool = &core.ToolCall{Name: "execute", Target: tool.Shell(), Detail: spec.Command, Reason: "scheduled probe"}
	m.mu.Unlock()
	choice, err := w.ConfirmCommand(fmt.Sprintf("Run the scheduled probe %q?", spec.Name), names)
	if err != nil {
		return core.TrustDeny, err
	}
	if choice == core.TrustPersist && len(names) > 0 {
		// The chat loop persists "always" in the role layer; a probe has no loop,
		// so it performs the same write here.
		if err := config.AddTrustedCmds(names...); err != nil {
			w.emit("error", "could not save trusted commands: "+err.Error())
		}
	}
	return choice, nil
}

// openWatchers lists outstanding watcher work for the situation. It is a
// projection of the persisted probe state, not a second queue: an item is open
// exactly while its fire has not been handled. Items whose event is already in
// this wake are skipped — the fired line describes them, and repeating them
// would read as two separate incidents.
func (m *Manager) openWatchers(w *worker, wake core.Wake) []openItem {
	wb, ok := m.backend.(ServiceWatcherBackend)
	if !ok {
		return nil
	}
	m.mu.Lock()
	id := w.sessionID
	m.mu.Unlock()
	if id == "" {
		return nil
	}
	watchers, err := wb.Watchers(id)
	if err != nil {
		return nil
	}
	inWake := make(map[string]bool, len(wake.Events))
	for _, ev := range wake.Events {
		if name, ok := strings.CutPrefix(ev.Source, "watch:"); ok {
			inWake[name] = true
		}
	}
	var open []openItem
	for _, ws := range watchers {
		if !ws.Enabled || inWake[ws.Name] {
			continue
		}
		if fired, yes := watcher.Open(ws.LastState); yes {
			open = append(open, openItem{Name: ws.Name, Fired: fired})
		}
	}
	return open
}

// nextServiceWake picks the next time the scheduler should look at this service:
// the earliest probe from the pass that just ran, or — for a backend that cannot
// persist probes — the manifest's own interval.
func (m *Manager) nextServiceWake(name string, probeNext *time.Time, now time.Time) *time.Time {
	if _, ok := m.backend.(ServiceWatcherBackend); ok {
		return probeNext
	}
	manifest := m.serviceManifest(name)
	if manifest == nil {
		return nil
	}
	if at, ok := manifest.NextWake(now); ok {
		return &at
	}
	return nil
}

// hasSubstantive reports whether a wake carries anything worth a model call. The
// scheduler's bare clock tick is not: the probes that ran at the head of the
// activation already turned it into an event when the world had changed.
func hasSubstantive(events []core.Event) bool {
	for _, ev := range events {
		if ev.Class == core.EventTimer && ev.Source == "clock" {
			continue
		}
		return true
	}
	return false
}

// mergeEvents folds b into a, dropping events a already carries by id and
// ordering the result the same way the scheduler does.
// mergeEvents folds b into a. For a repeated id the later occurrence wins: a
// probe that re-queued an event updates its payload in the store, and carrying
// the stale copy into the wake would hide the new observation.
func mergeEvents(a, b []core.Event) []core.Event {
	index := make(map[int64]int, len(a)+len(b))
	out := make([]core.Event, 0, len(a)+len(b))
	for _, ev := range append(append([]core.Event(nil), a...), b...) {
		if ev.ID != 0 {
			if i, ok := index[ev.ID]; ok {
				out[i] = ev
				continue
			}
			index[ev.ID] = len(out)
		}
		out = append(out, ev)
	}
	return core.PrepareEvents(out)
}

func earlier(a, b *time.Time) *time.Time {
	if a == nil {
		return cloneTime(b)
	}
	if b == nil {
		return a
	}
	if b.Before(*a) {
		return cloneTime(b)
	}
	return a
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
