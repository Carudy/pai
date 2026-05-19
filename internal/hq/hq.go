// Package hq provides shared types, constants, and logging used across PAI.
package hq

import (
	"fmt"
	"io"

	"github.com/Carudy/pai/internal/ui"
)

const PAI_VERSION = "v0.2.10"

// CliFlags holds all CLI flag values parsed from os.Args.
type CliFlags struct {
	Version bool
	Debug   bool
	Inter   bool
	Agent   string
	Input   string
}

// Logger provides level-aware logging for PAI.
// Debug messages go to slog; Error messages are styled and written to the
// supplied io.Writer so they appear inline with terminal output.
type Logger struct {
	Debug bool
}

// NewLogger creates a Logger. When debug is true, Debugf messages are emitted.
func NewLogger(debug bool) *Logger {
	return &Logger{Debug: debug}
}

// Debugf logs a debug-level message. No-op when debug is disabled.
// Use Errorf for user-facing errors (always visible).
func (l *Logger) Debugf(format string, a ...any) {
	if !l.Debug {
		return
	}
	msg := ui.RenderStr("Debug", "[DEBUG] "+fmt.Sprintf(format, a...))
	fmt.Println(msg)
}

// Errorf writes a styled error to w. This is always visible regardless of
// debug settings.
func Errorf(w io.Writer, format string, a ...any) {
	msg := ui.RenderStr("Error", "[Error] "+fmt.Sprintf(format, a...))
	fmt.Fprintln(w, msg)
}
