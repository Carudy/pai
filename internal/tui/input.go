package tui

import (
	"errors"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/Carudy/pai/internal/core"
)

func GetUserTextInput(prompt string) (string, error) {
	var value string
	err := huh.NewInput().
		Title(prompt).
		Value(&value).
		Validate(func(s string) error { return nil }).
		Run()
	if err != nil {
		return "", normalizePromptErr(err)
	}
	return strings.TrimSpace(value), nil
}

// normalizePromptErr maps a user-initiated abort onto core.ErrAborted so callers
// can treat "the user cancelled" as a clean stop rather than an error. huh reads
// Ctrl+C in raw mode as a keypress, not a signal, so this is the only place the
// interrupt surfaces while a prompt is active.
func normalizePromptErr(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		return core.ErrAborted
	}
	return err
}
