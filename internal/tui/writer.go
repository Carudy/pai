package tui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── Output routing ──────────────────────────────────────────────────────────

// lineMsg carries one committed output line from the agent to the UI, where it
// is printed above the live region.
type lineMsg struct{ text string }

// sessionMsg updates the session label shown in the live region.
type sessionMsg struct{ name string }

// tailMsg carries the line currently being written (no newline yet) so it can
// render inside the live region instead of waiting for the line to finish.
type tailMsg struct{ text string }

// tailInterval rate-limits live-tail refreshes. Command output arrives in very
// many small chunks and every refresh is a round-trip to the UI, so the tail is
// allowed to lag by at most a frame's worth of updates.
const tailInterval = 33 * time.Millisecond

// programWriter turns a byte stream into complete lines printed above the live
// region, plus a live tail for the partial line still being written.
//
// Until the program is running (and after it exits), writes fall back to the
// plain writer so early errors stay visible and shutdown can't strand a writer.
type programWriter struct {
	program  *tea.Program
	fallback io.Writer
	started  atomic.Bool

	mu     sync.Mutex
	carry  string    // bytes after the last newline
	tail   string    // most recent tail text handed to the UI
	tailAt time.Time // when that happened
}

func (w *programWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	lines, rest := splitLines(w.carry, string(p))
	w.carry = rest
	for _, ln := range lines {
		w.emitLine(ln)
	}
	// Finishing a line clears the tail at once; otherwise the update is coalesced.
	w.pushTail(rest, len(lines) > 0)
	return len(p), nil
}

func (w *programWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.carry != "" {
		w.emitLine(w.carry)
		w.carry = ""
	}
	w.pushTail("", true)
}

// splitLines returns the complete lines in carry+chunk plus the new carry. A
// carriage return restarts the line, so progress output ("10%\r20%\n") keeps
// only its final state rather than a transcript of every frame.
func splitLines(carry, chunk string) (lines []string, rest string) {
	s := carry + chunk
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			break
		}
		lines = append(lines, lastSegment(s[:i]))
		s = s[i+1:]
	}
	return lines, lastSegment(s)
}

// lastSegment returns a line's visible part: carriage return moves the cursor
// back to column 0, so only what follows the final one is ever displayed. A
// single trailing CR is the CRLF line ending and is simply dropped.
func lastSegment(line string) string {
	line = strings.TrimSuffix(line, "\r")
	if j := strings.LastIndexByte(line, '\r'); j >= 0 {
		return line[j+1:]
	}
	return line
}

func (w *programWriter) emitLine(line string) {
	if w.started.Load() && w.program != nil {
		// Send, not Println: Send is a no-op once the program has exited, whereas
		// Println would block forever on a channel nobody reads any more.
		w.program.Send(lineMsg{text: line})
		return
	}
	fmt.Fprintln(w.fallback, line)
}

// pushTail refreshes the live tail. force skips the rate limit, which is used
// when a line just completed so the tail clears immediately.
func (w *programWriter) pushTail(text string, force bool) {
	if text == w.tail && !force {
		return
	}
	if !force && text != "" && time.Since(w.tailAt) < tailInterval {
		return
	}
	w.tail = text
	w.tailAt = time.Now()
	if w.started.Load() && w.program != nil {
		w.program.Send(tailMsg{text: text})
	}
}

// markStarted enables UI routing. It forgets the remembered tail so the next
// update renders even if its text is unchanged from before startup.
func (w *programWriter) markStarted() {
	w.mu.Lock()
	w.tail = ""
	w.mu.Unlock()
	w.started.Store(true)
}

// markDead stops routing writes to the program once it has exited.
func (w *programWriter) markDead() { w.started.Store(false) }
