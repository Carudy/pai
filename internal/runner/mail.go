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
)

// sessionMailer binds the mail tool to its sender, so the tool never has to
// trust a session name supplied by the model.
type sessionMailer struct {
	m    *Manager
	from string
}

func (s sessionMailer) DeliverMail(to, text string, meta core.MailMeta) error {
	return s.m.deliverMail(s.from, to, text, meta)
}

var _ core.Mailer = sessionMailer{}

// deliverMail queues an EventPeer for a resident session and wakes it. The
// event is durable before the wake, so a stopped target, a busy target, or a
// crash between the two deliveries still receives it on the next activation.
// Mail is data, never a capability grant: the receiver's tools and
// confirmations are unchanged. The message carries the sender's trace forward
// and is refused once the chain is longer than core.MaxMailHops, which stops a
// two-session acknowledgement loop without asking the model to be polite.
//
// When a group directory is available, membership bounds delivery: a member may
// address its own group, and mail never crosses a group edge. `group:<name>`
// addresses the group's lead rather than broadcasting, so a group of N members
// has N addressed edges instead of N².
func (m *Manager) deliverMail(from, to, text string, meta core.MailMeta) error {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	text = strings.TrimSpace(text)
	switch {
	case from == "" || to == "":
		return errors.New("mail needs a sender and a recipient")
	case from == to:
		return errors.New("cannot mail the same session")
	case text == "":
		return errors.New("mail text is empty")
	case len(text) > maxMailBytes:
		return fmt.Errorf("mail is longer than %d bytes", maxMailBytes)
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
		return fmt.Errorf("this message would be hop %d of trace %s, and the limit is %d; answer instead of forwarding",
			hops, trace, core.MaxMailHops)
	}

	m.mu.Lock()
	fromID := m.sessionIDs[from]
	m.mu.Unlock()

	// A group address is an alias for its lead, resolved before the target rules
	// so the rest of delivery has one shape.
	groups, _ := m.backend.(GroupBackend)
	toName := to
	if name, ok := strings.CutPrefix(to, "group:"); ok {
		if groups == nil {
			return errors.New("group mail is unavailable: this host has no group directory")
		}
		g, found, err := groups.GroupByName(strings.TrimSpace(name))
		if err != nil {
			return fmt.Errorf("look up group %q: %w", name, err)
		}
		if !found {
			return fmt.Errorf("group %q does not exist", name)
		}
		if fromID == "" {
			return fmt.Errorf("only a member may address group %q", g.Name)
		}
		fromGroup, in, err := groups.GroupBySession(fromID)
		if err != nil {
			return fmt.Errorf("look up sender group: %w", err)
		}
		if !in || fromGroup.ID != g.ID {
			return fmt.Errorf("%s is not a member of group %q", from, g.Name)
		}
		if g.LeadName == "" {
			return fmt.Errorf("group %q has no lead session", g.Name)
		}
		if g.LeadName == from {
			return fmt.Errorf("group %q is led by this session, so there is no one to address", g.Name)
		}
		toName = g.LeadName
	}

	m.mu.Lock()
	id := m.sessionIDs[toName]
	known := m.serviceNames[toName]
	stopped := m.stoppedServices[toName]
	m.mu.Unlock()
	if !known || id == "" {
		return fmt.Errorf("%q is not a resident session", toName)
	}

	if groups != nil {
		if err := checkMembership(groups, from, fromID, toName, id); err != nil {
			m.diag(Diagnostic{Session: from, Kind: "mail", Subject: toName, Outcome: "refused",
				Detail: err.Error()})
			return err
		}
	}

	events, ok := m.backend.(ServiceEventBackend)
	if !ok {
		return errors.New("this backend cannot queue mail")
	}
	now := time.Now()
	if pending, err := events.DueEvents(id, now, maxMailPending+1); err == nil {
		count := 0
		for _, ev := range pending {
			if ev.Class == core.EventPeer {
				count++
			}
		}
		if count > maxMailPending {
			return fmt.Errorf("the mailbox for %q is full", toName)
		}
	}

	ev := core.Event{
		Session: id, Class: core.EventPeer, Kind: "mail",
		Source: "session:" + from, Payload: text, At: now,
		Trace: trace, Hops: hops,
	}
	eid, err := events.AppendEvent(id, ev)
	if err != nil {
		return fmt.Errorf("queue mail for %q: %w", toName, err)
	}
	ev.ID = eid
	m.diag(Diagnostic{Session: toName, Kind: "event", Outcome: "queued",
		Detail: fmt.Sprintf("mail from %s hop %d trace %s", from, hops, trace)})

	// A stopped session keeps the mail until it starts; StartService registers it
	// due now, and the startup drain sees the event.
	if stopped {
		return nil
	}
	wake := core.Wake{Reason: "mail from " + from, Events: []core.Event{ev}}
	if m.scheduler.Wake(toName, wake) {
		return nil
	}
	// A known service that was not registered yet (created outside StartServices):
	// registering and retrying once is enough; the event is already durable.
	// Register due now rather than dormant, so the event is still swept if the
	// second Wake loses a concurrent unregister/register race.
	m.scheduler.Register(toName, &now)
	m.scheduler.Wake(toName, wake)
	return nil
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
