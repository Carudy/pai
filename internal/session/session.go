// Package session persists chat sessions.
//
// Two backends satisfy Store, chosen at build time:
//
//	go build ./cmd/pai               # default: zero-dependency JSONL files
//	go build -tags sqlite ./cmd/pai  # pure-Go SQLite (adds ~3.6 MB stripped)
//
// Callers never branch on the backend.
package session

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"time"

	"github.com/Carudy/pai/internal/core"
)

var (
	// ErrNotFound is returned when a session does not exist.
	ErrNotFound = errors.New("session not found")
	// ErrExists is returned when creating or renaming onto a taken name.
	ErrExists = errors.New("session already exists")
)

// Meta describes a session without loading its turns.
type Meta struct {
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Model     string    `json:"model"`
	Cwd       string    `json:"cwd"`
	Title     string    `json:"title,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Turns     int       `json:"turns,omitempty"`
}

// Session is metadata plus turns, oldest first.
type Session struct {
	Meta  Meta
	Turns []core.Turn
}

// nameRe restricts session names so they are safe as filenames too.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ValidName reports whether name can be used as a session name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// NewName returns an auto-generated, collision-unlikely session name.
func NewName() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	suffix := make([]byte, 4)
	for i := range suffix {
		suffix[i] = alphabet[rand.IntN(len(alphabet))]
	}
	return time.Now().Format("20060102-150405") + "-" + string(suffix)
}

// validate returns an error if name is not usable.
func validate(name string) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid session name %q (allowed: letters, digits, . _ -)", name)
	}
	return nil
}

// Recorder appends turns to one session, implementing core.Recorder.
type Recorder struct {
	store Store
	name  string
}

// NewRecorder returns a core.Recorder writing to the named session.
func NewRecorder(store Store, name string) *Recorder {
	return &Recorder{store: store, name: name}
}

func (r *Recorder) AppendTurn(t core.Turn) error { return r.store.Append(r.name, t) }

var _ core.Recorder = (*Recorder)(nil)
