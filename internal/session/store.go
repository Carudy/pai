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
	// SetModel updates metadata without changing conversation history.
	SetModel(name, model string) error
	// SetRole updates the session's role without changing conversation history.
	SetRole(name, role string) error
	// Truncate keeps the first `keep` turns and drops the rest, or ErrNotFound.
	// It backs rewinding to an earlier point in a conversation.
	Truncate(name string, keep int) error
	// Rename renames a session, or ErrNotFound / ErrExists.
	Rename(oldName, newName string) error
	// Delete removes a session, or ErrNotFound.
	Delete(name string) error
	Close() error
}
