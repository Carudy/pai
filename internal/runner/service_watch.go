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
	id, dir, parked := w.sessionID, w.workingDir, w.parked
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

	manifest := m.serviceManifest(name)
	if manifest != nil && manifest.ReportInterval > 0 && !hasWatcher(watchers, config.ReportWatcherName) {
		if added, ok := m.ensureReportWatcher(wb, id, manifest.ReportInterval, now); ok {
			watchers = append(watchers, added)
		}
	}

	type observation struct {
		spec config.WatcherSpec
		res  watcher.Result
		next time.Time
	}
	var fired []observation
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

		if spec.Command != "" && ws.ApprovedAt == nil {
			if !m.probeTrusted(w, spec.Command) {
				// Scheduled code never runs on its own authority. Keeping the cadence
				// leaves the row visible as "awaiting approval" and ready to run the
				// moment it is granted, without spinning the scheduler.
				nextRun := now.Add(spec.Interval)
				if err := wb.SetWatcherState(ws.ID, ws.LastRun, &nextRun, ws.LastState, ws.LastError); err != nil {
					w.emit("error", fmt.Sprintf("could not reschedule watcher %q: %v", ws.Name, err))
				}
				next = earlier(next, &nextRun)
				m.diag(Diagnostic{Session: name, Kind: "watcher", Subject: ws.Name, Outcome: "awaiting approval",
					Detail: spec.Command, Next: nextRun})
				continue
			}
			// A name the session already trusts is a grant; record it instead of
			// re-deriving it on every probe.
			if err := wb.SetWatcherApproval(ws.ID, now, ws.NextRun); err != nil {
				w.emit("error", fmt.Sprintf("could not approve watcher %q: %v", ws.Name, err))
			} else {
				m.diag(Diagnostic{Session: name, Kind: "service", Subject: ws.Name, Outcome: "granted",
					Detail: "trusted command: " + spec.Command})
			}
		}

		res := watcher.Probe(probeCtx, spec, dir, ws.LastState, now)
		if probeCtx.Err() != nil {
			// The budget cut this probe short; that is not a probe failure.
			next = earlier(next, &now)
			m.diag(Diagnostic{Session: name, Kind: "watcher", Subject: ws.Name, Outcome: "deferred",
				Detail: "probe budget exhausted; retries on the next activation"})
			continue
		}
		nextRun := res.NextRun
		if err := wb.SetWatcherState(ws.ID, &now, &nextRun, res.State, errorText(res.Err)); err != nil {
			w.emit("error", fmt.Sprintf("could not persist watcher %q: %v", ws.Name, err))
		}
		next = earlier(next, &nextRun)
		if res.Fire {
			fired = append(fired, observation{spec: spec, res: res, next: nextRun})
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

	// report_on_change means "report only when something changed": the report
	// tick is a heartbeat, not news, so it is dropped when no other probe fired. Being
	// parked on a question suppresses it too — a heartbeat would only re-ask.
	changed := false
	for _, o := range fired {
		if o.spec.Name != config.ReportWatcherName {
			changed = true
			break
		}
	}

	events := make([]core.Event, 0, len(fired))
	for _, o := range fired {
		if o.spec.Name == config.ReportWatcherName {
			if manifest != nil && manifest.ReportOnChange && !changed {
				m.diag(Diagnostic{Session: name, Kind: "watcher", Subject: o.spec.Name, Outcome: "quiet",
					Detail: "report dropped: nothing changed", Next: o.next})
				continue
			}
			if parked {
				m.diag(Diagnostic{Session: name, Kind: "watcher", Subject: o.spec.Name, Outcome: "deferred",
					Detail: "report dropped: parked on a question", Next: o.next})
				continue
			}
		}
		ev := watcherEvent(id, o.spec, o.res, now)
		if eb, ok := m.backend.(ServiceEventBackend); ok {
			eid, err := eb.AppendEvent(id, ev)
			if err != nil {
				w.emit("error", fmt.Sprintf("could not queue watcher %q: %v", o.spec.Name, err))
				m.diag(Diagnostic{Session: name, Kind: "watcher", Subject: o.spec.Name, Outcome: "failed",
					Detail: "could not queue the event: " + err.Error(), Next: o.next})
				continue
			}
			ev.ID = eid
		}
		events = append(events, ev)
		m.diag(Diagnostic{Session: name, Kind: "watcher", Subject: o.spec.Name, Outcome: "fired",
			Detail: watcherDetail(o.spec, o.res), Next: o.next})
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
// clock tick with a name; a command probe is a condition change. Severity rides
// in the payload for now; mapping it to an event class is a later policy.
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
	if spec.Severity != "" && spec.Severity != "info" {
		payload = "[" + spec.Severity + "] " + payload
	}
	return core.Event{
		Session: sessionID, Class: class, Kind: kind, Source: "watch:" + spec.Name,
		Payload: payload, DedupKey: "watch:" + spec.Name, At: now,
	}
}

// ensureReportWatcher materializes service.report_interval as a timer watcher. It
// is a watcher rather than a special case so the heartbeat shares one persisted
// schedule, one due check, and one next-wake computation with every other probe.
func (m *Manager) ensureReportWatcher(wb ServiceWatcherBackend, sessionID string, every time.Duration, now time.Time) (WatcherState, bool) {
	spec := config.WatcherSpec{
		Name: config.ReportWatcherName, Interval: every,
		Timeout: min(10*time.Second, every),
		Prompt:  config.ReportPrompt,
	}
	at := now.Add(every)
	ws := WatcherState{
		SessionID: sessionID, Name: spec.Name, Spec: config.MarshalWatcherSpec(spec),
		Enabled: true, ApprovedAt: &now, NextRun: &at,
	}
	if err := wb.PutWatcher(ws); err != nil {
		return WatcherState{}, false
	}
	return ws, true
}

// dueEvents returns pending events whose retry time has come, so work survives a
// restart or a failed activation. The wake may already carry some of them; the
// caller dedupes by id.
func (m *Manager) dueEvents(w *worker, now time.Time) []core.Event {
	eb, ok := m.backend.(ServiceEventBackend)
	if !ok {
		return nil
	}
	m.mu.Lock()
	id := w.sessionID
	m.mu.Unlock()
	if id == "" {
		return nil
	}
	events, err := eb.DueEvents(id, now, maxDueEvents)
	if err != nil {
		w.emit("error", "could not read due events: "+err.Error())
		return nil
	}
	if len(events) > 0 {
		m.diag(Diagnostic{Session: w.name, Kind: "event", Outcome: "recovered",
			Detail: fmt.Sprintf("%d due event(s)", len(events))})
	}
	return events
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
	trusted := append([]string(nil), w.cfgTrusted...)
	trusted = append(trusted, commandTrustNames(m.commandTrust[w.name])...)
	m.mu.Unlock()
	return trustAll || tool.IsTrusted(command, trusted)
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
func mergeEvents(a, b []core.Event) []core.Event {
	seen := make(map[int64]bool, len(a)+len(b))
	out := make([]core.Event, 0, len(a)+len(b))
	for _, ev := range append(append([]core.Event(nil), a...), b...) {
		if ev.ID != 0 {
			if seen[ev.ID] {
				continue
			}
			seen[ev.ID] = true
		}
		out = append(out, ev)
	}
	return core.PrepareEvents(out)
}

func hasWatcher(list []WatcherState, name string) bool {
	for _, ws := range list {
		if ws.Name == name {
			return true
		}
	}
	return false
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
