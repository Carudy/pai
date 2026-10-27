package cli

import (
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Carudy/pai/internal/runner"
)

// serveDiagnosticLine renders one diagnostic as an audit-friendly line:
// timestamp, session, then the fact. Every variable field goes through
// serveActivityText, so a probe's multi-line output cannot break the one-line
// contract or inject terminal controls.
func serveDiagnosticLine(d runner.Diagnostic) string {
	var b strings.Builder
	b.WriteString(d.Time.Local().Format("2006-01-02T15:04:05"))
	b.WriteString(" [")
	b.WriteString(serveActivityText(d.Session))
	b.WriteString("] ")

	switch d.Kind {
	case "watcher":
		b.WriteString("watcher <")
		b.WriteString(serveActivityText(d.Subject))
		b.WriteString(">: ")
		if d.Outcome == "fired" || d.Outcome == "quiet" || d.Outcome == "held" {
			b.WriteString("result ")
			b.WriteString(strconv.Quote(serveActivityText(d.Detail)))
			if d.Outcome == "fired" {
				b.WriteString(", triggered")
			} else {
				b.WriteString(", not triggered")
			}
			if d.Reason != "" {
				b.WriteString(" (")
				b.WriteString(serveActivityText(d.Reason))
				b.WriteString(")")
			}
		} else {
			b.WriteString(serveActivityText(d.Outcome))
			if detail := serveActivityText(d.Detail); detail != "" {
				b.WriteString(": ")
				b.WriteString(detail)
			}
		}
	default:
		b.WriteString(serveActivityText(d.Kind))
		if d.Subject != "" {
			b.WriteString(" ")
			b.WriteString(serveActivityText(d.Subject))
		}
		if d.Outcome != "" {
			b.WriteString(" ")
			b.WriteString(serveActivityText(d.Outcome))
		}
		if detail := serveActivityText(d.Detail); detail != "" {
			b.WriteString(": ")
			b.WriteString(detail)
		}
	}
	if !d.Next.IsZero() {
		b.WriteString(", next ")
		b.WriteString(d.Next.Local().Format("15:04:05"))
	}
	b.WriteString("\n")
	return b.String()
}

// startServeDiagnostics streams the runner's host-side diagnostics to stdout.
// It mirrors the activity log: bounded, opt-in, and never holding shutdown
// hostage to a blocked writer.
func startServeDiagnostics(m *runner.Manager, stdout io.Writer) (func(), error) {
	diagnostics, unsubscribe, err := m.SubscribeDiagnostics()
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for d := range diagnostics {
			if _, err := io.WriteString(stdout, serveDiagnosticLine(d)); err != nil {
				unsubscribe()
				return
			}
		}
	}()
	return func() {
		unsubscribe()
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		}
	}, nil
}
