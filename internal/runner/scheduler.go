package runner

import (
	"context"
	"sync"
	"time"

	"github.com/Carudy/pai/internal/core"
)

// ServiceHost runs one activation for a resident session. The scheduler calls
// Activate at most once at a time per session; it must return when the
// activation ends and report the next timer wake (nil = wait for an event).
// Activate must honor ctx: a stopped scheduler must not leave a model call
// running.
type ServiceHost interface {
	Activate(ctx context.Context, name string, wake core.Wake) (*time.Time, error)
}

// Scheduler decides when resident sessions run. It never calls the model; the
// host does, under the scheduler's capacity limit. It owns the timer heap, the
// ready queue, and wake coalescing, so a timer probe and a peer event cannot
// start two activations for the same session.
type Scheduler struct {
	ctx    context.Context
	cancel context.CancelFunc
	host   ServiceHost
	max    int

	mu       sync.Mutex
	active   int
	sessions map[string]*scheduled
	ready    []*scheduled
	doneCh   chan activationEnd
	kick     chan struct{}
	wg       sync.WaitGroup
}

type scheduled struct {
	name    string
	next    *time.Time
	pending *core.Wake
	ready   bool
	running bool
}

type activationEnd struct {
	name string
	next *time.Time
	err  error
}

// NewScheduler starts the central scheduler. maxActive bounds activations that
// are actually running (model/tool work); resident sessions waiting for a wake
// do not consume that capacity. A service is registered before it can wake.
func NewScheduler(ctx context.Context, host ServiceHost, maxActive int) *Scheduler {
	if maxActive < 1 {
		maxActive = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Scheduler{
		ctx: ctx, cancel: cancel, host: host, max: maxActive,
		sessions: make(map[string]*scheduled),
		doneCh:   make(chan activationEnd, maxActive),
		kick:     make(chan struct{}, 1),
	}
	s.wg.Add(1)
	go s.run()
	return s
}

// Register adds or updates a service. next is its next timer wake (nil means it
// waits for an external event). Registration is idempotent.
func (s *Scheduler) Register(name string, next *time.Time) {
	s.mu.Lock()
	st := s.sessions[name]
	if st == nil {
		st = &scheduled{name: name}
		s.sessions[name] = st
	}
	st.next = cloneTime(next)
	s.mu.Unlock()
	s.signal()
}

// Unregister stops scheduling a service. A running activation is left to
// finish; the host owns its context and may cancel it separately.
func (s *Scheduler) Unregister(name string) {
	s.mu.Lock()
	delete(s.sessions, name)
	for i, st := range s.ready {
		if st.name == name {
			s.ready = append(s.ready[:i], s.ready[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
	s.signal()
}

// SetNext updates a registered service's next timer wake without waking it.
func (s *Scheduler) SetNext(name string, next *time.Time) {
	s.mu.Lock()
	if st := s.sessions[name]; st != nil {
		st.next = cloneTime(next)
	}
	s.mu.Unlock()
	s.signal()
}

// Wake delivers an event wake to a registered service. If an activation is
// already running or queued, the wake is merged into the next one: the host is
// never asked to run two activations for one session concurrently. It reports
// whether the service is registered.
func (s *Scheduler) Wake(name string, wake core.Wake) bool {
	s.mu.Lock()
	st := s.sessions[name]
	if st == nil {
		s.mu.Unlock()
		return false
	}
	st.pending = mergeWake(st.pending, wake)
	if !st.running && !st.ready {
		// An event supersedes the timer: there is work now, and the next timer is
		// recomputed from the watchers after the activation.
		st.next = nil
		s.enqueueLocked(st)
	}
	s.mu.Unlock()
	s.signal()
	return true
}

// Snapshot reports a service's scheduling state. It is for diagnostics and
// tests; it does not include the durable event queue.
func (s *Scheduler) Snapshot(name string) (running, queued bool, next *time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.sessions[name]
	if st == nil {
		return false, false, nil
	}
	return st.running, st.ready, cloneTime(st.next)
}

// Active reports how many activations are running.
func (s *Scheduler) Active() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Close stops the scheduler and waits for running activations to observe the
// canceled context.
func (s *Scheduler) Close() {
	s.cancel()
	s.wg.Wait()
}

func (s *Scheduler) run() {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		s.collectDueLocked(time.Now())
		s.dispatchLocked()
		wait := s.waitLocked()
		s.mu.Unlock()

		var timer <-chan time.Time
		if wait >= 0 {
			timer = time.After(wait)
		}
		select {
		case <-s.ctx.Done():
			return
		case <-s.kick:
		case end := <-s.doneCh:
			s.mu.Lock()
			s.finishLocked(end)
			s.mu.Unlock()
		case <-timer:
		}
	}
}

func (s *Scheduler) signal() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// collectDueLocked turns overdue timers into queued timer wakes. A service that
// is already running or queued keeps its pending work; the timer is folded into
// the next activation instead of starting a second one.
func (s *Scheduler) collectDueLocked(now time.Time) {
	for _, st := range s.sessions {
		if st.running || st.ready || st.next == nil || st.next.After(now) {
			continue
		}
		st.pending = mergeWake(st.pending, core.Wake{
			Reason: "timer",
			Events: []core.Event{{
				Class: core.EventTimer, Kind: "timer", Source: "clock", At: now,
			}},
		})
		st.next = nil
		s.enqueueLocked(st)
	}
}

func (s *Scheduler) dispatchLocked() {
	for s.active < s.max && len(s.ready) > 0 {
		st := s.ready[0]
		s.ready = s.ready[1:]
		st.ready = false
		if st.running {
			continue
		}
		st.running = true
		s.active++
		wake := core.Wake{}
		if st.pending != nil {
			wake = *st.pending
			st.pending = nil
		}
		s.wg.Add(1)
		go func(name string, wake core.Wake) {
			defer s.wg.Done()
			next, err := s.host.Activate(s.ctx, name, wake)
			select {
			case s.doneCh <- activationEnd{name: name, next: next, err: err}:
			case <-s.ctx.Done():
			}
		}(st.name, wake)
	}
}

func (s *Scheduler) finishLocked(end activationEnd) {
	if s.active > 0 {
		s.active--
	}
	st := s.sessions[end.name]
	if st == nil {
		return
	}
	st.running = false
	st.next = cloneTime(end.next)
	if st.pending != nil {
		// Work arrived while this activation ran: run it next, regardless of the
		// timer the activation returned.
		s.enqueueLocked(st)
	}
}

// waitLocked returns how long until the earliest timer, or -1 when there is no
// idle session with a timer.
func (s *Scheduler) waitLocked() time.Duration {
	var earliest *time.Time
	for _, st := range s.sessions {
		if st.running || st.ready || st.next == nil {
			continue
		}
		if earliest == nil || st.next.Before(*earliest) {
			earliest = st.next
		}
	}
	if earliest == nil {
		return -1
	}
	wait := time.Until(*earliest)
	if wait < 0 {
		return 0
	}
	return wait
}

func (s *Scheduler) enqueueLocked(st *scheduled) {
	if st.ready || st.running {
		return
	}
	st.ready = true
	s.ready = append(s.ready, st)
}

// mergeWake folds b into a, preserving event order and dedupe semantics. A
// non-empty situation or reason from the newer wake wins; the host renders the
// situation from the coalesced events in the next phase.
func mergeWake(a *core.Wake, b core.Wake) *core.Wake {
	if a == nil {
		copy := b
		copy.Events = core.PrepareEvents(append([]core.Event(nil), b.Events...))
		return &copy
	}
	a.Events = core.PrepareEvents(append(a.Events, b.Events...))
	if b.Situation != "" {
		a.Situation = b.Situation
	}
	if b.Reason != "" {
		a.Reason = b.Reason
	}
	a.Preempt = a.Preempt || b.Preempt
	return a
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	copy := *t
	return &copy
}
