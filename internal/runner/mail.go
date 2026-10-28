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

func (s sessionMailer) DeliverMail(to, text string) error {
	return s.m.deliverMail(s.from, to, text)
}

var _ core.Mailer = sessionMailer{}

// deliverMail queues an EventPeer for a resident session and wakes it. The
// event is durable before the wake, so a stopped target, a busy target, or a
// crash between the two deliveries still receives it on the next activation.
// Mail is data, never a capability grant: the receiver's tools and
// confirmations are unchanged.
func (m *Manager) deliverMail(from, to, text string) error {
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

	m.mu.Lock()
	id := m.sessionIDs[to]
	known := m.serviceNames[to]
	stopped := m.stoppedServices[to]
	m.mu.Unlock()
	if !known || id == "" {
		return fmt.Errorf("%q is not a resident session", to)
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
			return fmt.Errorf("the mailbox for %q is full", to)
		}
	}

	ev := core.Event{
		Session: id, Class: core.EventPeer, Kind: "mail",
		Source: "session:" + from, Payload: text, At: now,
	}
	eid, err := events.AppendEvent(id, ev)
	if err != nil {
		return fmt.Errorf("queue mail for %q: %w", to, err)
	}
	ev.ID = eid
	m.diag(Diagnostic{Session: to, Kind: "event", Outcome: "queued", Detail: "mail from " + from})

	// A stopped session keeps the mail until it starts; StartService registers it
	// due now, and the startup drain sees the event.
	if stopped {
		return nil
	}
	wake := core.Wake{Reason: "mail from " + from, Events: []core.Event{ev}}
	if m.scheduler.Wake(to, wake) {
		return nil
	}
	// A known service that was not registered yet (created outside StartServices):
	// registering and retrying once is enough; the event is already durable.
	m.scheduler.Register(to, nil)
	m.scheduler.Wake(to, wake)
	return nil
}
