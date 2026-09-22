package tui

import (
	"fmt"
	"io"
)

// Logger writes level-aware, styled messages to an io.Writer (typically stdout).
type Logger struct {
	Debug  bool
	writer io.Writer
}

// NewLogger creates a Logger. When debug is true, Debugf messages are emitted.
func NewLogger(w io.Writer, debug bool) *Logger {
	return &Logger{writer: w, Debug: debug}
}

// SetWriter redirects log output — used to route diagnostics into a live UI so
// they don't corrupt its rendering.
func (l *Logger) SetWriter(w io.Writer) { l.writer = w }

// Debugf logs a debug-level message. No-op unless Debug is set.
func (l *Logger) Debugf(format string, a ...any) {
	if !l.Debug {
		return
	}
	fmt.Fprintln(l.writer, RenderStr("Debug", "[DEBUG] "+fmt.Sprintf(format, a...)))
}

// Errorf logs a styled error. Always visible regardless of Debug.
func (l *Logger) Errorf(format string, a ...any) {
	fmt.Fprintln(l.writer, RenderStr("Error", "[Error] "+fmt.Sprintf(format, a...)))
}
