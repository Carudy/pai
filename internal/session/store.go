package session

import "github.com/Carudy/pai/internal/core"

// Store persists sessions. SQLite is the only implementation; see Open.
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
	// SetWorkingDir updates the workspace without changing conversation history.
	SetWorkingDir(name, cwd string) error
	// SetOverride stores or replaces the user's surgery on one turn, returning
	// ErrNotFound when the session or the turn does not exist.
	SetOverride(name string, o Override) error
	// DeleteOverride clears the surgery on one turn, or ErrNotFound when there is
	// none to clear.
	DeleteOverride(name string, seq int) error
	// Truncate keeps the first `keep` turns and drops the rest, or ErrNotFound.
	// It backs rewinding to an earlier point in a conversation.
	Truncate(name string, keep int) error
	// Rename renames a session, or ErrNotFound / ErrExists.
	Rename(oldName, newName string) error
	// Delete removes a session, or ErrNotFound.
	Delete(name string) error
	Close() error
}
