package cli

import (
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/runner"
)

func serveActivityLine(a runner.Activity) string {
	label := a.Event.Type
	parts := []string{label}
	switch label {
	case "tool_call":
		c, ok := a.Event.Data.(core.ToolCall)
		if !ok {
			return ""
		}
		parts = []string{"tool_exec", c.Name, c.Target}
		// Only commands are useful here; file content, diffs and queries stay private.
		if c.Name == "execute" || c.Name == "remote" {
			parts = append(parts, c.Detail)
		}
	case "prompt":
		if p, ok := a.Event.Data.(runner.Prompt); ok {
			parts = append(parts, p.Kind)
		}
	case "done", "terminate", "awaiting", "stopped", "prompt_cancelled", "prompt_replied":
	default:
		return ""
	}
	return "[" + serveActivityText(a.Name) + "]: " + serveActivityText(strings.Join(parts, " ")) + "\n"
}

func serveActivityText(s string) string {
	// Escape terminal controls and collapse newlines to keep each event one line.
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	runes := []rune(strings.Join(strings.Fields(s), " "))
	if len(runes) > 512 {
		return string(runes[:512]) + "…"
	}
	return string(runes)
}

func startServeActivityLog(m *runner.Manager, stdout io.Writer) (func(), error) {
	activities, unsubscribe, err := m.SubscribeActivities()
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for a := range activities {
			if line := serveActivityLine(a); line != "" {
				if _, err := io.WriteString(stdout, line); err != nil {
					unsubscribe()
					return
				}
			}
		}
	}()
	return func() {
		unsubscribe()
		// io.Writer has no cancellation contract. Drain normally, but do not
		// hold server shutdown hostage to a blocked stdout write.
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		}
	}, nil
}
