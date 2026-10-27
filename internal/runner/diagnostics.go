package runner

import "time"

// Diagnostic is one host-side fact an operator may want to audit: a probe's
// verdict, a scheduler decision, a model call's cost. It is deliberately
// separate from Activity: Activity is the live story a browser renders, while
// diagnostics repeat on every probe and would drown that stream.
type Diagnostic struct {
	Time    time.Time `json:"time"`
	Session string    `json:"session,omitempty"`
	Kind    string    `json:"kind"`              // watcher | activation | event | service | model | notice | error
	Subject string    `json:"subject,omitempty"` // the watcher or model the fact is about, when it has one
	Outcome string    `json:"outcome,omitempty"` // fired, quiet, failed, skipped, granted, …
	Detail  string    `json:"detail,omitempty"`  // what happened; the adapter collapses it to one line
	Reason  string    `json:"reason,omitempty"`  // why, when the outcome alone is ambiguous (e.g. "reminder not due")
	Next    time.Time `json:"next"`              // zero when nothing is scheduled
}

// SubscribeDiagnostics observes host-side diagnostics without replay. Like
// activities, the buffer is bounded and drops rather than delaying an agent:
// this is an audit trail, not a work queue.
func (m *Manager) SubscribeDiagnostics() (<-chan Diagnostic, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, ErrClosed
	}
	if len(m.diagnostics) >= subscriberLimit {
		return nil, nil, ErrCapacity
	}
	if m.diagnostics == nil {
		m.diagnostics = make(map[chan Diagnostic]struct{})
	}
	ch := make(chan Diagnostic, 256)
	m.diagnostics[ch] = struct{}{}
	return ch, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.diagnostics[ch]; ok {
			delete(m.diagnostics, ch)
			close(ch)
		}
	}, nil
}

// diag publishes one diagnostic. A zero Time is filled here so every emitter
// need not repeat it.
func (m *Manager) diag(d Diagnostic) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.diagLocked(d)
}

// diagLocked is diag for callers already holding m.mu — notably the event
// emitters, which must not re-enter the lock. It never blocks.
func (m *Manager) diagLocked(d Diagnostic) {
	if len(m.diagnostics) == 0 {
		return
	}
	if d.Time.IsZero() {
		d.Time = time.Now()
	}
	for ch := range m.diagnostics {
		select {
		case ch <- d:
		default:
		}
	}
}
