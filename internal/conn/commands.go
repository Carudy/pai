package conn

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/core"
)

const localHelp = `LOCAL COMMANDS
  /help                 Show this list
  /sessions             List sessions on the server
  /inspect [N]          Print the context to send (last N cards, default 40)
  /trust [on|off]       Show or set auto-approval for this session
  /model [provider:m]   Show models, or switch this session's model
  /role [name]          Show roles, or switch this session's role
  /dir [path]           Show or set this session's working directory
  /steer <text>         Redirect the running task at its next step
  /cancel               Stop the current step
  /quit                 Detach (Ctrl+C cancels the remote step and exits)

Anything else starting with / is forwarded to the server as an in-session
command; prefix // to send a literal instruction that begins with a slash.`

// local runs a client-side command and reports whether the session should end.
func (a *attach) local(ctx context.Context, line string) (quit bool) {
	fields := strings.Fields(line)
	args := fields[1:]
	switch fields[0] {
	case "/quit", "/exit":
		return true
	case "/help", "/h", "/?":
		fmt.Fprintln(a.out, localHelp)
	case "/sessions":
		if err := PrintSessions(ctx, a.client, a.out, a.name); err != nil {
			a.fail(err)
		}
	case "/inspect":
		limit := 40
		if len(args) > 0 {
			if n, err := strconv.Atoi(args[0]); err == nil && n > 0 && n <= 1000 {
				limit = n
			}
		}
		if err := PrintContext(ctx, a.client, a.name, limit, a.out); err != nil {
			a.fail(err)
		}
	case "/trust":
		a.trust(ctx, args)
	case "/model":
		a.model(ctx, args)
	case "/role":
		a.role(ctx, args)
	case "/dir":
		a.dir(ctx, args)
	case "/steer":
		text := strings.TrimSpace(strings.TrimPrefix(line, "/steer"))
		if text == "" {
			fmt.Fprintln(a.out, "usage: /steer <text>")
			return false
		}
		if err := a.client.Steer(ctx, a.name, text); err != nil {
			a.fail(err)
		} else {
			fmt.Fprintln(a.out, "steering: "+text)
		}
	case "/cancel":
		if err := a.client.Cancel(ctx, a.name); err != nil {
			a.fail(err)
		} else {
			fmt.Fprintln(a.out, "cancelled")
		}
	default:
		// Not local: forward it so the server's own /commands still work.
		if err := a.client.Send(ctx, a.name, line); err != nil {
			a.fail(err)
		}
	}
	return false
}

func (a *attach) fail(err error) { fmt.Fprintf(a.out, "error: %v\n", err) }

// replayTurns bounds the stored transcript printed on attach. Attaching is about
// picking the conversation back up, not dumping a long session; /inspect shows
// the exact window the next request would send.
const replayTurns = 80

// replay prints what a session already contains when it has no live run. It goes
// through the same Observer as live events, so the terminal renders it the same
// way, and it keeps only what a reader needs: instructions, the model's answers
// and questions, and summaries. Tool traffic is model-facing and often large, so
// it is left to /inspect.
func (a *attach) replay(ctx context.Context, total int) {
	offset := 0
	if total > replayTurns {
		offset = total - replayTurns
	}
	h, err := a.client.History(ctx, a.name, offset, replayTurns)
	if err != nil {
		a.fail(err)
		return
	}
	if len(h.Turns) == 0 {
		fmt.Fprintln(a.out, "(no stored turns — send an instruction to start a run)")
		return
	}
	if offset > 0 {
		fmt.Fprintf(a.out, "── stored transcript: last %d of %d turns ──\n", len(h.Turns), total)
	} else {
		fmt.Fprintf(a.out, "── stored transcript: %d turn(s) ──\n", len(h.Turns))
	}
	for _, t := range h.Turns {
		switch {
		case t.Role == "user" && t.Kind == core.KindInput:
			text := t.Content
			if t.Essential {
				text = "★ " + text
			}
			a.obs.User(text)
		case t.Kind == core.KindSummary:
			a.obs.Notice("[summary] " + clip(t.Content, 1200))
		case t.Role == "assistant" && t.Kind == core.KindOutput:
			a.replayAnswer(t.Content)
		}
	}
	fmt.Fprintln(a.out, "── end of stored transcript — type an instruction to continue ──")
}

// replayAnswer renders a stored assistant turn through the same parser the loop
// uses, so a replay and a live run agree on what the response says.
func (a *attach) replayAnswer(content string) {
	resp, err := chat.ParseResponse(content)
	if err != nil {
		a.obs.Done(clip(content, 4000))
		return
	}
	switch resp.Action {
	case chat.ActionDone:
		a.obs.Done(clip(resp.GetPayload(), 4000))
	case chat.ActionAsk:
		a.obs.Ask(clip(resp.GetPayload(), 2000))
	case chat.ActionTerminate:
		a.obs.Terminate(clip(resp.GetPayload(), 1000))
	}
}

// clip bounds a replay line by runes, keeping multibyte text intact.
func clip(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + fmt.Sprintf(" … (%d more chars)", len(runes)-n)
}

// PrintSessions renders the server's session list; current is marked with "*".
func PrintSessions(ctx context.Context, c *Client, out io.Writer, current string) error {
	list, total, err := c.Sessions(ctx, 0, 50)
	if err != nil {
		return err
	}
	if total > len(list) {
		fmt.Fprintf(out, "%d session(s), showing the first %d:\n", total, len(list))
	} else {
		fmt.Fprintf(out, "%d session(s):\n", total)
	}
	for _, m := range list {
		mark := " "
		if m.Name == current {
			mark = "*"
		}
		fmt.Fprintf(out, "%s %-24s %-10s %-24s %s\n", mark, m.Name, m.Role, m.Model, m.Cwd)
	}
	return nil
}

// PrintContext renders the last limit cards of the context a session would send.
func PrintContext(ctx context.Context, c *Client, name string, limit int, out io.Writer) error {
	probe, err := c.Context(ctx, name, 0, 1)
	if err != nil {
		return err
	}
	offset := probe.Total - limit
	if offset < 0 {
		offset = 0
	}
	view := probe
	if offset > 0 || probe.Total > 1 {
		if view, err = c.Context(ctx, name, offset, limit); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "context: %s · keep %d turns · %d messages · compact would fold %d turn(s)\n",
		view.Model, view.Config.KeepTurns, view.Total, view.CompactTurns)
	if len(view.Cards) == 0 {
		fmt.Fprintln(out, "(no stored turns)")
		return nil
	}
	for _, card := range view.Cards {
		star := ""
		if card.Essential {
			star = " ★"
		}
		text := card.Sent
		if text == "" {
			text = card.Original
		}
		fmt.Fprintf(out, "#%-4d %-9s %-11s %-14s%s %s\n", card.Seq, card.Role, card.Kind, card.State, star, firstLine(text))
	}
	return nil
}

func (a *attach) trust(ctx context.Context, args []string) {
	if len(args) == 0 {
		on, err := a.client.TrustAll(ctx, a.name)
		if err != nil {
			a.fail(err)
			return
		}
		fmt.Fprintf(a.out, "trust-all: %v\n", on)
		return
	}
	// A typo must not silently mean "off": auto-approval is a safety switch.
	var on bool
	switch strings.ToLower(args[0]) {
	case "on", "true", "1", "yes":
		on = true
	case "off", "false", "0", "no":
		on = false
	default:
		fmt.Fprintln(a.out, "usage: /trust [on|off]")
		return
	}
	if err := a.client.SetTrustAll(ctx, a.name, on); err != nil {
		a.fail(err)
		return
	}
	fmt.Fprintf(a.out, "trust-all: %v\n", on)
}

func (a *attach) model(ctx context.Context, args []string) {
	if len(args) == 0 {
		models, def, err := a.client.Models(ctx)
		if err != nil {
			a.fail(err)
			return
		}
		fmt.Fprintf(a.out, "model: %s (default %s)\noptions: %s\n", a.meta.Model, def, strings.Join(models, ", "))
		return
	}
	if err := a.client.SetModel(ctx, a.name, args[0]); err != nil {
		a.fail(err)
		return
	}
	a.meta.Model = args[0]
	fmt.Fprintf(a.out, "model: %s (applies to the next run)\n", args[0])
}

func (a *attach) role(ctx context.Context, args []string) {
	if len(args) == 0 {
		roles, def, err := a.client.Roles(ctx)
		if err != nil {
			a.fail(err)
			return
		}
		fmt.Fprintf(a.out, "role: %s (default %s)\noptions: %s\n", a.meta.Role, def, strings.Join(roles, ", "))
		return
	}
	if err := a.client.SetRole(ctx, a.name, args[0]); err != nil {
		a.fail(err)
		return
	}
	a.meta.Role = args[0]
	fmt.Fprintf(a.out, "role: %s (applies to the next run)\n", args[0])
}

func (a *attach) dir(ctx context.Context, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(a.out, "working dir: %s\n", a.meta.Cwd)
		return
	}
	if err := a.client.SetWorkingDir(ctx, a.name, args[0]); err != nil {
		a.fail(err)
		return
	}
	a.meta.Cwd = args[0]
	fmt.Fprintf(a.out, "working dir: %s (applies to the next run)\n", args[0])
}

// firstLine is a one-line preview for the inspector's text view.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if runes := []rune(s); len(runes) > 160 {
		s = string(runes[:160]) + "…"
	}
	if s == "" {
		return "(empty)"
	}
	return s
}
