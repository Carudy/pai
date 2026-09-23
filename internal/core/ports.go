// Package core defines the ports the agent loop needs from its host.
//
// It has no dependency on presentation or storage: adapters (tui, webui,
// session, ...) implement these interfaces and cli wires them together. Events
// carried across the Observer port are semantic — styling never crosses it, so
// a different front-end can render the same events differently.
package core

import (
	"errors"
	"io"
	"time"
)

// ErrAborted is returned by a Prompter when the user cancels a prompt (Ctrl+C,
// Ctrl+D) rather than the prompt failing. Callers treat it as a clean stop.
var ErrAborted = errors.New("prompt aborted")

// Kind values for Turn.Kind (and provider.Message.Kind).
const (
	KindInput      = "input"
	KindOutput     = "output"
	KindToolResult = "tool_result"
	KindUserAnswer = "user_answer"
	KindNote       = "note"
	// KindSummary marks a model-written summary that stands in for older turns.
	KindSummary = "summary"
)

// Turn is one conversation turn, as persisted and replayed.
type Turn struct {
	Role    string // "user" | "assistant"
	Kind    string // see Kind* above
	Content string
	At      time.Time
}

// Recorder persists conversation turns. A nil Recorder means "ephemeral".
type Recorder interface {
	AppendTurn(t Turn) error
}

// Sessions gives the agent loop control over where the conversation is stored,
// so in-session commands can name a temporary conversation, rename it, or start
// a fresh one. It is nil when no store is configured; the commands then report
// that rather than failing.
type Sessions interface {
	// Persist makes the conversation durable under name, backfilling turns when
	// it was not being stored, and returns the recorder for later turns.
	Persist(name string, turns []Turn) (Recorder, error)
	// New starts a fresh conversation, optionally named, returning its recorder
	// (nil when the new conversation is ephemeral).
	New(name string) (Recorder, error)
}

// Usage is token accounting for a single model call.
type Usage struct {
	Prompt     int
	Completion int
	Total      int
}

// ToolCall describes a tool about to run.
type ToolCall struct {
	Name    string // "execute" | "remote" | "websearch"
	Target  string // shell name or SSH host (empty for websearch)
	Detail  string // the command or query
	Reason  string // why the model chose it
	Trusted bool   // ran without confirmation
}

// ToolResult is the outcome reported after a tool call.
type ToolResult struct {
	OK      bool   // succeeded
	Skipped bool   // user declined to run it
	Message string // one-line status
	Detail  string // optional secondary content (e.g. a search answer/preview)
}

// Observer receives semantic events from the agent loop.
type Observer interface {
	// Agent channel.
	Reason(text string)      // the model's stated reason
	Done(summary string)     // task completed
	Terminate(reason string) // task cannot complete
	Ask(question string)     // about to ask the user
	Awaiting()               // waiting for the next instruction
	User(text string)        // echo of the user's input
	Session(name string)     // the conversation's storage changed ("" = ephemeral)

	// Streaming and accounting.
	Reasoning(delta string)
	Usage(u Usage)

	// Tool channel.
	ToolCall(c ToolCall)
	ToolOutput(chunk string)
	ToolResult(r ToolResult)

	// Miscellaneous.
	Notice(text string) // non-fatal warning
	Output(text string) // informational output, e.g. a command's result
	Separator()
}

// Prompter asks the user for input. Implementations may block (line mode) or
// queue asynchronously (TUI with steering).
type Prompter interface {
	Ask(title string) (string, error)
	Confirm(title string) (bool, error)
}

// Logger is the loop's diagnostic sink.
type Logger interface {
	Debugf(format string, a ...any)
	Errorf(format string, a ...any)
}

// WriterFunc adapts a function to io.Writer, so a tool's streamed output can be
// routed through an Observer.
type WriterFunc func([]byte) (int, error)

func (f WriterFunc) Write(p []byte) (int, error) { return f(p) }

var _ io.Writer = WriterFunc(nil)
