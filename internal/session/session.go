// Package session persists chat sessions in pure-Go SQLite
// (modernc.org/sqlite), so the build needs no C toolchain.
//
// Callers never branch on the storage engine. Open takes exclusive, nonblocking
// OS ownership of the entire data directory. Even readers must wait until the
// owning Store is closed. This prevents duplicate session runtimes across
// processes. Process exit (including a crash) releases ownership; the persistent
// sessions.lock file is not a stale lock and must not be removed while a store is
// open.
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

	// PromptTokens and PromptEstimate are the last request's prompt size as the
	// provider reported it, and the size PAI's estimator predicted for that same
	// prompt. Their ratio is how the estimate is calibrated against a real tokenizer;
	// see TokenScale. Zero means no request has reported yet.
	PromptTokens   int `json:"prompt_tokens,omitempty"`
	PromptEstimate int `json:"prompt_estimate,omitempty"`
}

// TokenScale converts PAI's token estimate into the provider's accounting, learned
// from the last request (reported / estimated for the same prompt). It returns 1 when
// there is nothing to learn from, and is clamped so one odd report cannot move a
// budget by more than a factor of two.
func (m Meta) TokenScale() float64 {
	if m.PromptTokens <= 0 || m.PromptEstimate <= 0 {
		return 1
	}
	scale := float64(m.PromptTokens) / float64(m.PromptEstimate)
	return min(max(scale, 0.5), 2)
}

// Session is metadata plus turns, oldest first.
type Session struct {
	Meta  Meta
	Turns []core.Turn
	// Overrides is the user's context surgery keyed by Turn.Seq; nil when none.
	// It changes only what a runtime sends, never the stored turn.
	Overrides map[int]Override
}

// Override is a user's surgery on one stored turn, keyed by Turn.Seq. The turn
// itself is never rewritten.
type Override struct {
	Seq     int
	Action  string // core.OverrideReplace | core.OverrideDrop
	Content string // replacement text for OverrideReplace; empty for OverrideDrop
}

// nameRe restricts session names so they stay portable identifiers, safe in
// filenames and unambiguous as CLI arguments.
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

// SetUsage records the last request's token accounting, which is what calibrates the
// token estimate (see Meta.TokenScale).
func (r *Recorder) SetUsage(reported, estimate int) error {
	return r.store.SetUsage(r.name, reported, estimate)
}

var (
	_ core.Recorder      = (*Recorder)(nil)
	_ core.UsageRecorder = (*Recorder)(nil)
)
