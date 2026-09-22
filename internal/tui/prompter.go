package tui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Carudy/pai/internal/core"
)

// Prompter is the line-mode adapter for core.Prompter, used when there is no
// terminal to drive (pipes, redirected output). It reads whole lines, so there
// is no raw mode: Ctrl+C keeps its usual signal meaning, and EOF ends the
// prompt. Interactive terminals get the App instead, where prompts are modal
// and Ctrl+C is handled as a keypress.
type Prompter struct {
	r   *bufio.Reader
	out io.Writer
}

// NewPrompter returns a line prompter reading from in and writing prompts to out.
func NewPrompter(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{r: bufio.NewReader(in), out: out}
}

var _ core.Prompter = (*Prompter)(nil)

// Ask writes the title and returns the next line, trimmed. EOF reports
// core.ErrAborted so callers treat "no more input" as a clean stop.
func (p *Prompter) Ask(title string) (string, error) {
	if _, err := fmt.Fprintf(p.out, "%s ", RenderStr("Warn", title)); err != nil {
		return "", err
	}
	line, err := p.readLine()
	// Nothing echoes the answer when input is a pipe, so close the prompt line
	// ourselves before the next output arrives.
	fmt.Fprintln(p.out)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// Confirm asks a yes/no question, defaulting to no. EOF is treated as "no"
// rather than an error: nothing was confirmed, so nothing should run.
func (p *Prompter) Confirm(title string) (bool, error) {
	if _, err := fmt.Fprintf(p.out, "%s %s ", RenderStr("Warn", title), RenderStr("Hint", "[y/N]")); err != nil {
		return false, nil
	}
	line, err := p.readLine()
	fmt.Fprintln(p.out)
	if err != nil {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// readLine returns one line without its newline, mapping a bare EOF onto
// core.ErrAborted. A final line with no trailing newline is still returned.
func (p *Prompter) readLine() (string, error) {
	line, err := p.r.ReadString('\n')
	switch {
	case err == nil:
		return strings.TrimRight(line, "\r\n"), nil
	case errors.Is(err, io.EOF) && line != "":
		return strings.TrimRight(line, "\r\n"), nil
	case errors.Is(err, io.EOF):
		return "", core.ErrAborted
	default:
		return "", err
	}
}
