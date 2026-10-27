// Package watcher evaluates a service's host-side probes.
//
// A probe is a bounded command (or a plain tick) that answers one question:
// does the watched condition hold? The command prints 1/0 (or true/false); the
// rest of its output is the summary handed to the model when the watch fires.
// Keeping the decision in the command means the manifest owns no query
// language, and a probe can use whatever shell, Python, or jq is on the host.
// Nothing here knows about sessions, scheduling, or the model.
package watcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/tool"
)

const (
	// maxObservation bounds what a probe may contribute to the model's context,
	// and to the stored state, so a chatty command cannot grow either.
	maxObservation = 4 << 10
	// maxSummary caps the one-line event payload rendered into the situation.
	maxSummary = 512
)

// Result is one probe's outcome. State is the opaque value to persist; Fire says
// whether this observation is worth waking the model for; Err reports a probe
// failure, which is persisted but does not itself stop the watcher.
type Result struct {
	Fire     bool
	State    string
	Summary  string
	Severity string
	Err      error
	NextRun  time.Time
}

// state is what a watcher remembers between probes. It is JSON so it survives
// in the store's single last_state column: Since when the predicate started
// holding, Fired when the model was last woken, Reported whether this episode
// has already fired (the latch that makes the default once-per-episode), and
// Error the last probe failure.
type state struct {
	Since    int64  `json:"since,omitempty"`
	Fired    int64  `json:"fired,omitempty"`
	Reported bool   `json:"reported,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Probe runs one watcher once. prev is the watcher's last persisted State; now
// is the probe time, so scheduling stays testable.
func Probe(ctx context.Context, spec config.WatcherSpec, dir, prev string, now time.Time) Result {
	every := spec.Interval
	if every < config.MinWatchInterval {
		every = config.MinWatchInterval
	}
	res := Result{Severity: spec.Severity, NextRun: now.Add(every)}

	if spec.Command == "" {
		res.Fire = true
		res.State = prev
		res.Summary = spec.Prompt
		if res.Summary == "" {
			res.Summary = "scheduled tick"
		}
		return res
	}

	st := decodeState(prev)
	holds, summary, err := observe(ctx, spec, dir)
	if err != nil {
		return failed(spec, st, err, now, res)
	}

	next := state{Fired: st.Fired, Reported: st.Reported}
	if !holds {
		// The episode ended. Clearing the latch lets the next hold fire again
		// without needing an explicit repeat interval.
		next.Since, next.Reported = 0, false
		res.State = encodeState(next)
		return res
	}

	since := st.Since
	if since == 0 {
		since = now.Unix()
	}
	next.Since = since
	ready := now.Sub(time.Unix(since, 0)) >= spec.HoldFor

	allowed := false
	if spec.RepeatAfter > 0 {
		// A repeating condition re-alerts on its own interval, regardless of how
		// many probes observed it in between.
		allowed = st.Fired == 0 || now.Sub(time.Unix(st.Fired, 0)) >= spec.RepeatAfter
	} else {
		// No repeat interval: once per continuous episode. This is the safe
		// default; otherwise a level condition would wake the model on every
		// probe forever.
		allowed = !st.Reported
	}
	if ready && allowed {
		res.Fire = true
		res.Summary = summaryOr(summary)
		next.Fired = now.Unix()
		next.Reported = true
	}
	res.State = encodeState(next)
	return res
}

// failed reports a probe failure. A distinct failure wakes the model once (so a
// silently broken probe does not go unnoticed), while the same failure repeats
// only into last_error. The hold window and the episode latch survive, so a
// transient failure neither restarts the debounce nor re-alerts.
func failed(spec config.WatcherSpec, st state, cause error, now time.Time, res Result) Result {
	msg := truncate(cause.Error(), maxSummary)
	res.Err = cause
	res.Fire = st.Error != msg
	res.Summary = msg
	next := state{Since: st.Since, Fired: st.Fired, Reported: st.Reported, Error: msg}
	if res.Fire {
		next.Fired = now.Unix()
	}
	res.State = encodeState(next)
	return res
}

// observe runs the probe and returns its decision plus the summary text. A
// command that cannot answer (nonzero exit, timeout, empty output, or an
// unrecognized decision token) is a probe failure, not a quiet "no".
func observe(ctx context.Context, spec config.WatcherSpec, dir string) (bool, string, error) {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = min(10*time.Second, spec.Interval)
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Probes run without a confirmation prompt: the command was approved when
	// the service was created, and a resident check cannot wait for a human.
	res, err := tool.ExecuteCommandAt(ctx, spec.Command, dir, nil)
	if err != nil {
		return false, "", err
	}
	if res.TimedOut {
		return false, "", fmt.Errorf("timed out after %s", timeout)
	}
	if res.ExitCode != 0 {
		return false, "", fmt.Errorf("exit %d: %s", res.ExitCode, truncate(strings.TrimSpace(res.Output), 200))
	}
	out := strings.TrimSpace(res.Output)
	if out == "" {
		return false, "", errors.New("probe printed no decision")
	}
	if len(out) > maxObservation {
		out = truncate(out, maxObservation)
	}
	holds, summary, ok := splitDecision(out)
	if !ok {
		return false, "", fmt.Errorf("probe decision must be 1/0 or true/false, got %q", firstToken(out))
	}
	return holds, summary, nil
}

// splitDecision reads the boolean on the first line and everything after the
// token as the summary. Additional lines are appended, so a probe can print a
// short decision followed by a richer explanation.
func splitDecision(out string) (holds bool, summary string, ok bool) {
	line, rest, _ := strings.Cut(out, "\n")
	line = strings.TrimSpace(line)
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false, "", false
	}
	switch strings.ToLower(fields[0]) {
	case "1", "true":
		holds = true
	case "0", "false":
		holds = false
	default:
		return false, "", false
	}
	var parts []string
	if tail := strings.TrimSpace(strings.TrimPrefix(line, fields[0])); tail != "" {
		parts = append(parts, tail)
	}
	if extra := strings.TrimSpace(rest); extra != "" {
		parts = append(parts, extra)
	}
	return holds, truncate(strings.Join(parts, "\n"), maxSummary), true
}

func firstToken(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return truncate(fields[0], 40)
}

// summaryOr keeps the situation readable when the probe printed only a decision.
func summaryOr(summary string) string {
	if strings.TrimSpace(summary) == "" {
		return "triggered"
	}
	return summary
}

func decodeState(s string) state {
	var st state
	if s != "" {
		_ = json.Unmarshal([]byte(s), &st)
	}
	return st
}

func encodeState(st state) string {
	encoded, err := json.Marshal(st)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}
