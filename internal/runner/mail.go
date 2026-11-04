package runner

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Carudy/pai/internal/core"
)

const (
	// maxMailBytes bounds one message. Mail enters another session's prompt, so a
	// large paste would be paid for twice (sender and receiver).
	maxMailBytes = 8 << 10
	// maxMailPending is a best-effort mailbox bound, checked against due peer
	// events. It stops two sessions from flooding each other; the durable queue
	// remains the authority.
	maxMailPending = 32
	// A group broadcast is the one message that costs N wakes, so it is rate
	// limited per sender. This is not a wake budget: it bounds outbound fan-out,
	// not what a member may do with its own schedule.
	broadcastWindow        = 10 * time.Minute
	maxBroadcastsPerWindow = 10
	// A directed pair may exchange this many messages per window. The hop cap
	// bounds how long a chain may run; this bounds how fast two sessions can talk,
	// which a polite two-party handshake can otherwise do indefinitely.
	quarantineWindow    = 10 * time.Minute
	maxPerPairPerWindow = 20
)

// quarantineEntry counts the messages one directed pair sent inside the window,
// and remembers whether the refusal was already reported. The report is per
// episode, not per message: a loop must not turn its own diagnostics into load.
type quarantineEntry struct {
	times    []time.Time
	reported bool
}

// sessionMailer binds the mail tool to its sender, so the tool never has to
// trust a session name supplied by the model.
type sessionMailer struct {
	m    *Manager
	from string
}

func (s sessionMailer) DeliverMail(to, text string, meta core.MailMeta) (int, error) {
	return s.m.deliverMail(s.from, to, text, meta)
}

var _ core.Mailer = sessionMailer{}

// deliverMail queues one message for a resident session, or one copy per member
// when the address is a group. The event is durable before the wake, so a
// stopped target, a busy target, or a crash between the two deliveries still
// receives it on the next activation. Mail is data, never a capability grant:
// the receiver's tools and confirmations are unchanged. The message carries the
// sender's trace forward and is refused once the chain is longer than
// core.MaxMailHops, which stops a two-session acknowledgement loop without
// asking the model to be polite.
//
// When a group directory is available, membership bounds delivery: a member may
// address its own group, and mail never crosses a group edge.
func (m *Manager) deliverMail(from, to, text string, meta core.MailMeta) (int, error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	text = strings.TrimSpace(text)
	switch {
	case from == "" || to == "":
		return 0, errors.New("mail needs a sender and a recipient")
	case from == to:
		return 0, errors.New("cannot mail the same session")
	case text == "":
		return 0, errors.New("mail text is empty")
	case len(text) > maxMailBytes:
		return 0, fmt.Errorf("mail is longer than %d bytes", maxMailBytes)
	}

	trace := strings.TrimSpace(meta.Trace)
	if trace == "" {
		trace = core.NewTrace()
	}
	hops := meta.Hops
	if hops <= 0 {
		hops = 1
	}
	if hops > core.MaxMailHops {
		m.diag(Diagnostic{Session: from, Kind: "mail", Subject: to, Outcome: "refused",
			Detail: fmt.Sprintf("hop %d exceeds %d (trace %s)", hops, core.MaxMailHops, trace)})
		return 0, fmt.Errorf("this message would be hop %d of trace %s, and the limit is %d; answer instead of forwarding",
			hops, trace, core.MaxMailHops)
	}

	m.mu.Lock()
	fromID := m.sessionIDs[from]
	m.mu.Unlock()

	if name, ok := strings.CutPrefix(to, "group:"); ok {
		return m.broadcast(from, fromID, strings.TrimSpace(name), text, trace, hops)
	}

	m.mu.Lock()
	id := m.sessionIDs[to]
	known := m.serviceNames[to]
	m.mu.Unlock()
	if !known || id == "" {
		return 0, fmt.Errorf("%q is not a resident session", to)
	}
	if groups, ok := m.backend.(GroupBackend); ok {
		if err := checkMembership(groups, from, fromID, to, id); err != nil {
			m.diag(Diagnostic{Session: from, Kind: "mail", Subject: to, Outcome: "refused", Detail: err.Error()})
			return 0, err
		}
	}
	if err := m.mailboxHasRoom(id, to); err != nil {
		return 0, err
	}
	if err := m.admit(from, []string{to}, to); err != nil {
		return 0, err
	}
	ev := core.Event{
		Session: id, Class: core.EventPeer, Kind: "mail",
		Source: "session:" + from, Payload: text, At: time.Now(),
		Trace: trace, Hops: hops,
	}
	if err := m.queueMail(to, ev); err != nil {
		return 0, err
	}
	m.diag(Diagnostic{Session: to, Kind: "event", Outcome: "queued",
		Detail: fmt.Sprintf("mail from %s hop %d trace %s", from, hops, trace)})
	return 1, nil
}

// broadcast fans one message out to every other member. It is all-or-nothing: a
// full mailbox on any target refuses the whole send, so a group is never
// half-told. Each copy carries the sender's trace and hop count, so a
// group-wide reply chain dies at the same limit as addressed mail.
func (m *Manager) broadcast(from, fromID, name, text, trace string, hops int) (int, error) {
	groups, ok := m.backend.(GroupBackend)
	if !ok {
		return 0, errors.New("group mail is unavailable: this host has no group directory")
	}
	g, found, err := groups.GroupByName(name)
	if err != nil {
		return 0, fmt.Errorf("look up group %q: %w", name, err)
	}
	if !found {
		return 0, fmt.Errorf("group %q does not exist", name)
	}
	if fromID == "" {
		return 0, fmt.Errorf("only a member may address group %q", g.Name)
	}
	fromGroup, in, err := groups.GroupBySession(fromID)
	if err != nil {
		return 0, fmt.Errorf("look up sender group: %w", err)
	}
	if !in || fromGroup.ID != g.ID {
		return 0, fmt.Errorf("%s is not a member of group %q", from, g.Name)
	}
	members, err := groups.GroupMembers(g.ID)
	if err != nil {
		return 0, fmt.Errorf("read members of group %q: %w", g.Name, err)
	}
	targets := make([]core.GroupMember, 0, len(members))
	for _, mem := range members {
		if mem.SessionID != fromID && mem.Name != from {
			targets = append(targets, mem)
		}
	}
	if len(targets) == 0 {
		return 0, fmt.Errorf("group %q has no other members", g.Name)
	}
	if !m.broadcastAllowed(from) {
		m.diag(Diagnostic{Session: from, Kind: "mail", Subject: "group:" + g.Name, Outcome: "refused",
			Detail: fmt.Sprintf("more than %d broadcasts in %s", maxBroadcastsPerWindow, broadcastWindow)})
		return 0, fmt.Errorf("too many group broadcasts in %s; wait before sending another", broadcastWindow)
	}
	// Check every target before queueing anything: a broadcast is one action, so
	// a full mailbox must not produce a partial delivery.
	for _, t := range targets {
		if err := m.mailboxHasRoom(t.SessionID, t.Name); err != nil {
			return 0, fmt.Errorf("%v; the broadcast was not sent", err)
		}
	}
	// The pair cap also applies per recipient, so a large group cannot be used to
	// exceed what an addressed message to the same peer would allow. One refusal
	// refuses the send, for the same all-or-nothing reason as a full mailbox.
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, t.Name)
	}
	if err := m.admit(from, names, "group:"+g.Name); err != nil {
		return 0, fmt.Errorf("%v; the broadcast was not sent", err)
	}
	now := time.Now()
	for _, t := range targets {
		ev := core.Event{
			Session: t.SessionID, Class: core.EventPeer, Kind: "broadcast",
			Source: "session:" + from, Payload: text, At: now,
			Trace: trace, Hops: hops, Priority: core.PriorityBroadcast,
		}
		if err := m.queueMail(t.Name, ev); err != nil {
			return 0, fmt.Errorf("queue broadcast for %q: %w", t.Name, err)
		}
	}
	m.diag(Diagnostic{Session: from, Kind: "mail", Subject: "group:" + g.Name, Outcome: "queued",
		Detail: fmt.Sprintf("broadcast to %d member(s) hop %d trace %s", len(targets), hops, trace)})
	return len(targets), nil
}

// admit counts one message per recipient for the directed pairs from→recipient,
// or refuses the whole batch. Nothing is counted on a refusal, so a blocked
// broadcast cannot push an unrelated pair over the edge. subject is what the one
// diagnostic names: the recipient for addressed mail, the group for a broadcast.
func (m *Manager) admit(from string, tos []string, subject string) error {
	if bad, first := m.admitSends(from, tos); bad != "" {
		if first {
			m.diag(Diagnostic{Session: from, Kind: "mail", Subject: subject, Outcome: "refused",
				Detail: fmt.Sprintf("more than %d messages to %s in %s", maxPerPairPerWindow, bad, quarantineWindow)})
		}
		return fmt.Errorf("too many messages from %s to %s in %s; wait for the window to drain rather than resending",
			from, bad, quarantineWindow)
	}
	return nil
}

// admitSends is the quarantine itself. It returns the recipient that is over the
// limit and whether this is the first refusal of the episode.
func (m *Manager) admitSends(from string, tos []string) (string, bool) {
	now := time.Now()
	m.quarantineMu.Lock()
	defer m.quarantineMu.Unlock()
	if m.quarantine == nil {
		m.quarantine = make(map[string]*quarantineEntry)
	}
	// Expire quiet pairs here rather than on a timer: the map is small, and a
	// session that has gone silent should become reachable again immediately.
	for key, e := range m.quarantine {
		e.times = withinWindow(e.times, now)
		switch {
		case len(e.times) == 0:
			delete(m.quarantine, key)
		case len(e.times) < maxPerPairPerWindow:
			e.reported = false
		}
	}
	for _, to := range tos {
		if e := m.quarantine[pairKey(from, to)]; e != nil && len(e.times) >= maxPerPairPerWindow {
			first := !e.reported
			e.reported = true
			return to, first
		}
	}
	for _, to := range tos {
		key := pairKey(from, to)
		e := m.quarantine[key]
		if e == nil {
			e = &quarantineEntry{}
			m.quarantine[key] = e
		}
		e.times = append(e.times, now)
	}
	return "", false
}

func pairKey(from, to string) string { return from + "\x00" + to }

func withinWindow(times []time.Time, now time.Time) []time.Time {
	kept := times[:0]
	for _, at := range times {
		if now.Sub(at) < quarantineWindow {
			kept = append(kept, at)
		}
	}
	return kept
}

// mailboxHasRoom applies the pending cap to one recipient.
func (m *Manager) mailboxHasRoom(id, name string) error {
	events, ok := m.backend.(ServiceEventBackend)
	if !ok {
		return nil
	}
	pending, err := events.DueEvents(id, time.Now(), maxMailPending+1)
	if err != nil {
		return nil
	}
	count := 0
	for _, ev := range pending {
		if ev.Class == core.EventPeer {
			count++
		}
	}
	if count > maxMailPending {
		return fmt.Errorf("the mailbox for %q is full", name)
	}
	return nil
}

// queueMail appends the event and wakes the target unless it is stopped.
func (m *Manager) queueMail(toName string, ev core.Event) error {
	events, ok := m.backend.(ServiceEventBackend)
	if !ok {
		return errors.New("this backend cannot queue mail")
	}
	eid, err := events.AppendEvent(ev.Session, ev)
	if err != nil {
		return fmt.Errorf("queue mail for %q: %w", toName, err)
	}
	ev.ID = eid
	m.mu.Lock()
	stopped := m.stoppedServices[toName]
	m.mu.Unlock()
	if stopped {
		// A stopped session keeps the mail until it starts; StartService
		// registers it due now, and the startup drain sees the event.
		return nil
	}
	wake := core.Wake{Reason: mailWakeReason(ev), Events: []core.Event{ev}}
	if m.scheduler.Wake(toName, wake) {
		return nil
	}
	// A known service that was not registered yet (created outside
	// StartServices): registering and retrying once is enough; the event is
	// already durable. Register due now rather than dormant, so the event is
	// still swept if the second Wake loses a concurrent unregister race.
	now := time.Now()
	m.scheduler.Register(toName, &now)
	m.scheduler.Wake(toName, wake)
	return nil
}

func mailWakeReason(ev core.Event) string {
	from := strings.TrimPrefix(ev.Source, "session:")
	if ev.Kind == "broadcast" {
		return "group broadcast from " + from
	}
	return "mail from " + from
}

// broadcastAllowed applies the per-sender rate limit.
func (m *Manager) broadcastAllowed(from string) bool {
	now := time.Now()
	m.broadcastMu.Lock()
	defer m.broadcastMu.Unlock()
	if m.broadcasts == nil {
		m.broadcasts = make(map[string][]time.Time)
	}
	kept := m.broadcasts[from][:0]
	for _, at := range m.broadcasts[from] {
		if now.Sub(at) < broadcastWindow {
			kept = append(kept, at)
		}
	}
	if len(kept) >= maxBroadcastsPerWindow {
		m.broadcasts[from] = kept
		return false
	}
	m.broadcasts[from] = append(kept, now)
	return true
}

// checkMembership enforces the group boundary. Two in-group sessions may talk;
// two ungrouped sessions keep the pre-group behaviour; anything crossing an edge
// is refused. Mail is the only path this bounds: a user instruction and a
// session's own watchers are unaffected.
func checkMembership(groups GroupBackend, from, fromID, toName, toID string) error {
	var fromGroup, toGroup core.Group
	fromIn, toIn := false, false
	if fromID != "" {
		g, ok, err := groups.GroupBySession(fromID)
		if err != nil {
			return fmt.Errorf("look up sender group: %w", err)
		}
		fromGroup, fromIn = g, ok
	}
	g, ok, err := groups.GroupBySession(toID)
	if err != nil {
		return fmt.Errorf("look up recipient group: %w", err)
	}
	toGroup, toIn = g, ok

	switch {
	case fromIn && toIn && fromGroup.ID == toGroup.ID:
		return nil
	case fromIn && toIn:
		return fmt.Errorf("%s is in group %q and %s is in %q; mail does not cross group edges",
			from, fromGroup.Name, toName, toGroup.Name)
	case fromIn:
		return fmt.Errorf("%s is in group %q, so it may not mail %s, which is outside it",
			from, fromGroup.Name, toName)
	case toIn:
		return fmt.Errorf("%s belongs to group %q, so only its members may mail it", toName, toGroup.Name)
	default:
		return nil
	}
}
