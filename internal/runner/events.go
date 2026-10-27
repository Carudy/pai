package runner

type Event struct {
	Type     string   `json:"type"`
	Data     any      `json:"data,omitempty"`
	Snapshot Snapshot `json:"snapshot"`
}

// Activity identifies a live event independently of per-session subscribers.
type Activity struct {
	Name  string
	Event Event
}

// Subscribe atomically installs a subscriber and delivers a snapshot. Slow
// subscribers are disconnected rather than blocking the runtime; reconnect to
// recover current state and use History to recover durable turns.
func (m *Manager) Subscribe(name string) (<-chan Event, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, ErrClosed
	}
	w := m.entries[name]
	if w == nil {
		return nil, nil, ErrNotFound
	}
	if len(w.subs) >= subscriberLimit {
		return nil, nil, ErrCapacity
	}
	ch := make(chan Event, eventLimit)
	w.subs[ch] = struct{}{}
	ch <- Event{Type: "snapshot", Snapshot: w.snapshotLocked()}
	return ch, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := w.subs[ch]; ok {
			delete(w.subs, ch)
			close(ch)
		}
	}, nil
}

// SubscribeActivities observes all future worker events without replay. Its
// bounded buffer drops new events when full, never delaying agents. Unsubscribe
// is idempotent; Close closes the channel after workers finish emitting.
func (m *Manager) SubscribeActivities() (<-chan Activity, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, ErrClosed
	}
	if len(m.activities) >= subscriberLimit {
		return nil, nil, ErrCapacity
	}
	if m.activities == nil {
		m.activities = make(map[chan Activity]struct{})
	}
	ch := make(chan Activity, 128)
	m.activities[ch] = struct{}{}
	return ch, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.activities[ch]; ok {
			delete(m.activities, ch)
			close(ch)
		}
	}, nil
}

func (w *worker) eventLocked(kind string, data any) Event {
	if p, ok := data.(Prompt); ok {
		data = *clonePrompt(&p)
	}
	snapshot := w.snapshotLocked()
	// Deltas already carry reasoning; only recovery snapshots need the full tail.
	snapshot.Reasoning = ""
	return Event{Type: kind, Data: data, Snapshot: snapshot}
}

func (w *worker) emitLocked(kind string, data any) {
	if kind != "reasoning" && kind != "notice" {
		w.reasoning = ""
	}
	// Errors, notices and questions are the events an operator most wants in the
	// audit log: a retry, a stalled stream, a failed write, or a service parked
	// waiting for an answer. A pending prompt is the commonest reason a resident
	// session looks alive but does nothing.
	switch kind {
	case "error", "notice", "ask":
		if text, ok := data.(string); ok {
			w.m.diagLocked(Diagnostic{Session: w.name, Kind: kind, Detail: text})
		}
	case "prompt":
		if p, ok := data.(Prompt); ok {
			// Title may carry the full command; the tool's name and target are the
			// activity log's level of detail, so keep the same boundary here.
			detail := p.Title
			if p.Tool != nil {
				detail = p.Tool.Name + " " + p.Tool.Target
			}
			w.m.diagLocked(Diagnostic{Session: w.name, Kind: "prompt", Outcome: p.Kind, Detail: detail})
		}
	}
	for ch := range w.m.activities {
		select {
		case ch <- Activity{Name: w.name, Event: w.eventLocked(kind, data)}:
		default:
		}
	}
	for ch := range w.subs {
		// Each recipient owns its payload; mutating one event cannot change the
		// pending approval or another subscriber's reconnect snapshot.
		e := w.eventLocked(kind, data)
		select {
		case ch <- e:
		default:
			delete(w.subs, ch)
			close(ch)
		}
	}
}
func (w *worker) emit(kind string, data any) {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	w.emitLocked(kind, data)
}
