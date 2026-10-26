package runner

import (
	"context"

	"github.com/Carudy/pai/internal/core"
)

// ServiceInbox is the worker side of one resident activation. The host pushes
// the wake the scheduler chose; role.RunService blocks in Next, runs the
// activation, and calls Done. Buffer 1 is deliberate: the scheduler merges a
// second event into the next activation rather than queueing unbounded work.
type ServiceInbox struct {
	wakes chan core.Wake
	done  func(core.Wake, error) error
}

// NewServiceInbox creates the inbox. done is called exactly once per wake; a
// nil callback is allowed for a driver that has no durable side to acknowledge.
func NewServiceInbox(buffer int, done func(core.Wake, error) error) *ServiceInbox {
	if buffer < 1 {
		buffer = 1
	}
	return &ServiceInbox{wakes: make(chan core.Wake, buffer), done: done}
}

// Push offers a wake to the driver without blocking. false means a wake is
// already pending; the scheduler merges events instead of stacking activations.
func (i *ServiceInbox) Push(w core.Wake) bool {
	select {
	case i.wakes <- w:
		return true
	default:
		return false
	}
}

// Next blocks for the next wake or until the activation context ends.
func (i *ServiceInbox) Next(ctx context.Context) (core.Wake, error) {
	select {
	case w := <-i.wakes:
		return w, nil
	case <-ctx.Done():
		return core.Wake{}, ctx.Err()
	}
}

// Poll is the step-boundary check: it never blocks.
func (i *ServiceInbox) Poll() (core.Wake, bool) {
	select {
	case w := <-i.wakes:
		return w, true
	default:
		return core.Wake{}, false
	}
}

// Done acknowledges the wake and reports the activation's result.
func (i *ServiceInbox) Done(w core.Wake, err error) error {
	if i.done == nil {
		return nil
	}
	return i.done(w, err)
}

var _ core.Inbox = (*ServiceInbox)(nil)
