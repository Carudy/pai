package tui

import "github.com/Carudy/pai/internal/core"

// Prompter is the line-mode adapter for core.Prompter. It blocks the caller
// while the user types, which is fine for line mode; a TUI adapter can instead
// queue answers asynchronously to support steering.
type Prompter struct{}

// NewPrompter returns a blocking, line-mode prompter.
func NewPrompter() Prompter { return Prompter{} }

func (Prompter) Ask(title string) (string, error)   { return GetUserTextInput(title) }
func (Prompter) Confirm(title string) (bool, error) { return GetUserConfirm(title) }

var _ core.Prompter = Prompter{}
