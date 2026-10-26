// Package watcher evaluates a service's host-side probes.
//
// A probe is a bounded command (or a plain tick) whose observation is compared
// against a trigger. The caller persists the resulting State and wakes the model
// only when the observation crossed the trigger, so an unchanged world costs no
// model call. Nothing here knows about sessions, scheduling, or the model.
package watcher

import (
	"bytes"
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
// whether this observation is worth waking the model; Err reports a probe
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
// in the store's single last_state column: Value is the comparison baseline,
// Pending the value being debounced for a "changed" trigger, Since when the
// current condition started holding, Fired when the model was last woken, and
// Error the last probe failure.
type state struct {
	Value   string `json:"value,omitempty"`
	Pending string `json:"pending,omitempty"`
	Since   int64  `json:"since,omitempty"`
	Fired   int64  `json:"fired,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Probe runs one watcher once. prev is the watcher's last persisted State; now
// is the probe time, so scheduling stays testable.
func Probe(ctx context.Context, spec config.WatcherSpec, dir, prev string, now time.Time) Result {
	every := spec.Every
	if every < config.MinWatchInterval {
		every = config.MinWatchInterval
	}
	res := Result{Severity: spec.Severity, NextRun: now.Add(every)}

	if spec.Kind == "timer" {
		res.Fire = true
		res.State = prev
		res.Summary = "periodic check"
		return res
	}

	st := decodeState(prev)
	obs, err := observe(ctx, spec, dir)
	if err != nil {
		return failed(spec, st, err, now, res)
	}

	holds, pending, detail, err := evaluate(spec.Trigger, obs, st)
	if err != nil {
		return failed(spec, st, err, now, res)
	}

	since := st.Since
	switch {
	case !holds:
		since, pending = 0, ""
	case since == 0 || pending != st.Pending:
		// The condition (or, for a change, the new value) just started.
		since = now.Unix()
	}
	ready := holds && now.Sub(time.Unix(since, 0)) >= spec.Debounce
	cooled := spec.Cooldown <= 0 || st.Fired == 0 || now.Sub(time.Unix(st.Fired, 0)) >= spec.Cooldown

	next := state{Value: obs, Pending: pending, Since: since, Fired: st.Fired}
	if spec.Trigger == nil || spec.Trigger.Kind == "changed" {
		// For a change trigger the stored value is the comparison baseline, not the
		// last observation: it moves only when the change is reported.
		next.Value = st.Value
		if next.Value == "" {
			next.Value = obs
		}
	}
	if ready && cooled {
		res.Fire = true
		res.Summary = truncate(spec.Name+": "+detail, maxSummary)
		next.Fired = now.Unix()
		if spec.Trigger == nil || spec.Trigger.Kind == "changed" {
			// Rebase a change trigger on what was just reported; the condition is
			// consumed, so the next change needs a fresh debounce window.
			next.Value, next.Pending, next.Since = obs, "", 0
		}
	}
	res.State = encodeState(next)
	return res
}

// failed reports a probe failure. A distinct failure wakes the model once (so a
// silently broken probe does not go unnoticed), while the same failure repeats
// only into last_error.
func failed(spec config.WatcherSpec, st state, cause error, now time.Time, res Result) Result {
	msg := truncate(cause.Error(), maxSummary)
	res.Err = cause
	res.Fire = st.Error != msg
	res.Summary = truncate(spec.Name+": probe failed: "+msg, maxSummary)
	next := state{Value: st.Value, Error: msg}
	if res.Fire {
		next.Fired = now.Unix()
	} else {
		next.Pending, next.Since, next.Fired = st.Pending, st.Since, st.Fired
	}
	res.State = encodeState(next)
	return res
}

// observe runs the probe command and returns its canonical observation.
func observe(ctx context.Context, spec config.WatcherSpec, dir string) (string, error) {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = min(10*time.Second, spec.Every)
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, err := tool.ExecuteCommandAt(ctx, spec.Cmd, dir, nil)
	if err != nil {
		return "", err
	}
	if res.TimedOut {
		return "", fmt.Errorf("timed out after %s", timeout)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("exit %d: %s", res.ExitCode, truncate(strings.TrimSpace(res.Output), 200))
	}
	out := strings.TrimSpace(res.Output)
	if out == "" {
		return "", errors.New("probe produced no output")
	}
	if len(out) > maxObservation {
		out = out[:maxObservation]
	}
	// Whitespace-only reformatting is not a change: compact valid JSON so a
	// pretty-printed probe does not wake the model on every run.
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(out)) == nil {
		out = compact.String()
	}
	return out, nil
}

// evaluate reports whether the observation crossed the trigger. pending is the
// observed value a "changed" trigger is debouncing.
func evaluate(t *config.WatchTrigger, obs string, st state) (holds bool, pending, detail string, err error) {
	if t == nil || t.Kind == "changed" {
		if st.Value == "" {
			return false, "", "first observation", nil
		}
		if obs == st.Value {
			return false, "", "unchanged", nil
		}
		return true, obs, "changed to " + truncate(obs, 200), nil
	}

	value, err := fieldValue(obs, t.Field)
	if err != nil {
		return false, "", "", err
	}
	holds, detail, err = matches(t, value)
	return holds, "", detail, err
}

// fieldValue walks a dot-separated path into a JSON observation. UseNumber keeps
// integers and floats distinguishable until they are compared.
func fieldValue(obs, field string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(obs))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("probe output is not JSON: %w", err)
	}
	if field == "" {
		return root, nil
	}
	cur := root
	for _, seg := range strings.Split(field, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("field %q is not an object", field)
		}
		cur, ok = obj[seg]
		if !ok {
			return nil, fmt.Errorf("field %q is missing", field)
		}
	}
	return cur, nil
}

func matches(t *config.WatchTrigger, value any) (bool, string, error) {
	switch t.Kind {
	case "json_number":
		got, ok := number(value)
		if !ok {
			return false, "", fmt.Errorf("field %q is not a number", t.Field)
		}
		want, ok := number(t.Value)
		if !ok {
			return false, "", errors.New("trigger value is not a number")
		}
		detail := fmt.Sprintf("%s = %v (%s %v)", t.Field, got, t.Op, want)
		switch t.Op {
		case ">":
			return got > want, detail, nil
		case ">=":
			return got >= want, detail, nil
		case "<":
			return got < want, detail, nil
		case "<=":
			return got <= want, detail, nil
		case "==":
			return got == want, detail, nil
		case "!=":
			return got != want, detail, nil
		}
	case "json_string":
		got, ok := value.(string)
		if !ok {
			return false, "", fmt.Errorf("field %q is not a string", t.Field)
		}
		want, ok := t.Value.(string)
		if !ok {
			return false, "", errors.New("trigger value is not a string")
		}
		detail := fmt.Sprintf("%s = %q (%s %q)", t.Field, got, t.Op, want)
		switch t.Op {
		case "==":
			return got == want, detail, nil
		case "!=":
			return got != want, detail, nil
		case "contains":
			return strings.Contains(got, want), detail, nil
		}
	case "json_bool":
		got, ok := value.(bool)
		if !ok {
			return false, "", fmt.Errorf("field %q is not a bool", t.Field)
		}
		want, ok := t.Value.(bool)
		if !ok {
			return false, "", errors.New("trigger value is not a bool")
		}
		detail := fmt.Sprintf("%s = %v (%s %v)", t.Field, got, t.Op, want)
		switch t.Op {
		case "==":
			return got == want, detail, nil
		case "!=":
			return got != want, detail, nil
		}
	}
	return false, "", fmt.Errorf("unsupported trigger %s %q", t.Kind, t.Op)
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	}
	return 0, false
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
	return s[:n] + "…"
}
