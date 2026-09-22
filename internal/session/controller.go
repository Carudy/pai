package session

import (
	"fmt"
	"sync"

	"github.com/Carudy/pai/internal/core"
)

// Controller implements core.Sessions on top of a Store for the in-session
// commands. A conversation that is not being stored keeps its turns in memory
// (the loop passes them back on Persist), so naming a temporary run can
// backfill everything that happened before it was named.
//
// The store is opened lazily: a run that is never named never touches disk.
type Controller struct {
	mu     sync.Mutex
	open   func() (Store, error) // nil = persistence unavailable
	store  Store                 // non-nil once the conversation is stored
	owned  bool                  // we opened store, so we must close it
	name   string
	metaFn func() Meta // fresh metadata for newly created sessions
}

// NewController returns a controller persisting through open. store may be nil
// (a temporary run), in which case it is opened on first use. metaFn supplies
// the role/model/cwd for sessions created later, which may have changed by then.
func NewController(open func() (Store, error), store Store, name string, metaFn func() Meta) *Controller {
	return &Controller{open: open, store: store, name: name, metaFn: metaFn}
}

var _ core.Sessions = (*Controller)(nil)

// Persist names (or renames) the conversation. Naming a run that was not being
// stored creates the session and writes the supplied turns into it.
func (c *Controller) Persist(name string, turns []core.Turn) (core.Recorder, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !ValidName(name) {
		return nil, fmt.Errorf("invalid session name %q (allowed: letters, digits, . _ -)", name)
	}

	if c.store != nil {
		if err := c.store.Rename(c.name, name); err != nil {
			return nil, err
		}
		c.name = name
		return NewRecorder(c.store, name), nil
	}

	store, err := c.ensureStore()
	if err != nil {
		return nil, err
	}
	meta := c.metaFn()
	meta.Name = name
	if _, err := store.Create(meta); err != nil {
		return nil, err
	}
	if len(turns) > 0 {
		if err := store.Append(name, turns...); err != nil {
			return nil, err
		}
	}
	c.name = name
	return NewRecorder(store, name), nil
}

// New starts a fresh conversation. An empty name makes it ephemeral again,
// releasing any store this controller opened itself.
func (c *Controller) New(name string) (core.Recorder, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if name == "" {
		c.release()
		c.name = ""
		return nil, nil
	}
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid session name %q (allowed: letters, digits, . _ -)", name)
	}

	store, err := c.ensureStore()
	if err != nil {
		return nil, err
	}
	meta := c.metaFn()
	meta.Name = name
	if _, err := store.Create(meta); err != nil {
		return nil, err
	}
	c.name = name
	return NewRecorder(store, name), nil
}

// Name returns the conversation's current session name ("" when ephemeral).
func (c *Controller) Name() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.name
}

// Close releases a store this controller opened. It does not close a store the
// caller supplied.
func (c *Controller) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owned && c.store != nil {
		err := c.store.Close()
		c.store, c.owned, c.name = nil, false, ""
		return err
	}
	return nil
}

func (c *Controller) ensureStore() (Store, error) {
	if c.store != nil {
		return c.store, nil
	}
	if c.open == nil {
		return nil, fmt.Errorf("session storage is unavailable")
	}
	store, err := c.open()
	if err != nil {
		return nil, err
	}
	c.store, c.owned = store, true
	return store, nil
}

// release drops an owned store, leaving a caller-supplied one alone.
func (c *Controller) release() {
	if c.owned && c.store != nil {
		_ = c.store.Close()
	}
	if c.owned {
		c.store, c.owned = nil, false
	}
}
