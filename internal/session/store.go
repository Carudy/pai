package session

import "github.com/Carudy/pai/internal/core"

// Store persists sessions. Exactly one backend is compiled in; see Open.
type Store interface {
	// Create stores a new session. It returns ErrExists if the name is taken.
	Create(meta Meta) (*Session, error)
	// Get loads a session by name, or ErrNotFound.
	Get(name string) (*Session, error)
	// Latest returns the most recently updated session for cwd (all sessions
	// when cwd is empty), or ErrNotFound.
	Latest(cwd string) (*Session, error)
	// List returns all session metadata, most recently updated first.
	List() ([]Meta, error)
	// Append adds turns to an existing session, or ErrNotFound.
	Append(name string, turns ...core.Turn) error
	// Rename renames a session, or ErrNotFound / ErrExists.
	Rename(oldName, newName string) error
	// Delete removes a session, or ErrNotFound.
	Delete(name string) error
	Close() error
}
