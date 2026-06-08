// Package hq provides shared types, constants, and logging used across PAI.
package hq

import (
	"fmt"
	"io"

	"github.com/Carudy/pai/internal/ui"
)

const PAI_VERSION = "v0.4.1"

// CliFlags holds all CLI flag values parsed from os.Args.
type CliFlags struct {
	Version bool
	Debug   bool
	Inter   bool
	Agent   string
	Input   string
}

// Logger provides level-aware, styled logging for PAI.
// Both debug and error messages are written to the io.Writer supplied at
// construction (typically os.Stdout for CLI output).
type Logger struct {
	Debug  bool
	writer io.Writer
}

// NewLogger creates a Logger. When debug is true, Debugf messages are emitted.
func NewLogger(w io.Writer, debug bool) *Logger {
	return &Logger{writer: w, Debug: debug}
}

// Debugf logs a debug-level message. No-op when Debug is false.
func (l *Logger) Debugf(format string, a ...any) {
	if !l.Debug {
		return
	}
	msg := ui.RenderStr("Debug", "[DEBUG] "+fmt.Sprintf(format, a...))
	fmt.Fprintln(l.writer, msg)
}

// Errorf logs a styled error message. Always visible regardless of debug
// settings.
func (l *Logger) Errorf(format string, a ...any) {
	msg := ui.RenderStr("Error", "[Error] "+fmt.Sprintf(format, a...))
	fmt.Fprintln(l.writer, msg)
}
