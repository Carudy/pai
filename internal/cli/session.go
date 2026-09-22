package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/paths"
	"github.com/Carudy/pai/internal/provider"
	"github.com/Carudy/pai/internal/session"
	"github.com/Carudy/pai/internal/tui"
)

// resolveSession opens the session store and determines which session this run
// uses, returning the turns to resume. An empty name means "ephemeral": no
// store is opened and nothing is persisted.
//
// Precedence: --attach (must exist) > --session (create or continue) >
// --continue (latest for this directory) > config [session] persist (auto-named).
func resolveSession(cfg *config.UserConfig, flags CliFlags) (session.Store, string, []provider.Message, error) {
	if flags.Attach == "" && flags.Session == "" && !flags.Continue && !cfg.SessionPersist {
		return nil, "", nil, nil
	}

	store, err := session.Open()
	if err != nil {
		return nil, "", nil, err
	}

	cwd, _ := os.Getwd()
	meta := session.Meta{Role: cfg.DefaultRole, Model: cfg.DefaultModel, Cwd: cwd}

	var sess *session.Session
	switch {
	case flags.Attach != "":
		sess, err = store.Get(flags.Attach)
		if err != nil {
			store.Close()
			return nil, "", nil, fmt.Errorf("attach: %w", err)
		}
	case flags.Session != "":
		meta.Name = flags.Session
		sess, err = store.Get(flags.Session)
		if errors.Is(err, session.ErrNotFound) {
			sess, err = store.Create(meta)
		}
		if err != nil {
			store.Close()
			return nil, "", nil, fmt.Errorf("session: %w", err)
		}
	case flags.Continue:
		sess, err = store.Latest(cwd)
		if err != nil {
			store.Close()
			return nil, "", nil, fmt.Errorf("continue: %w", err)
		}
	default: // cfg.SessionPersist
		meta.Name = session.NewName()
		sess, err = store.Create(meta)
		if err != nil {
			store.Close()
			return nil, "", nil, err
		}
	}

	turns := sess.Turns
	if cfg.SessionMaxTurns > 0 && len(turns) > cfg.SessionMaxTurns {
		turns = turns[len(turns)-cfg.SessionMaxTurns:]
	}
	return store, sess.Meta.Name, toMessages(turns), nil
}

// toMessages converts persisted turns into a replayable conversation.
func toMessages(turns []core.Turn) []provider.Message {
	msgs := make([]provider.Message, 0, len(turns))
	for _, t := range turns {
		msgs = append(msgs, provider.Message{Role: t.Role, Content: t.Content})
	}
	return msgs
}

// runSession handles `pai session <list|show|rm|rename>`.
func runSession(args []string, stdout io.Writer, log *tui.Logger) int {
	if len(args) == 0 {
		args = []string{"list"}
	}

	store, err := session.Open()
	if err != nil {
		log.Errorf("Error opening session store: %v\n", err)
		return 1
	}
	defer store.Close()

	switch args[0] {
	case "list", "ls":
		return sessionList(store, stdout, log)

	case "show":
		if len(args) < 2 {
			log.Errorf("Usage: pai session show <name>\n")
			return 1
		}
		return sessionShow(store, args[1], stdout, log)

	case "rm", "delete":
		if len(args) < 2 {
			log.Errorf("Usage: pai session rm <name>\n")
			return 1
		}
		if err := store.Delete(args[1]); err != nil {
			log.Errorf("Error: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Deleted session %q.\n", args[1])
		return 0

	case "rename", "mv":
		if len(args) < 3 {
			log.Errorf("Usage: pai session rename <old> <new>\n")
			return 1
		}
		if err := store.Rename(args[1], args[2]); err != nil {
			log.Errorf("Error: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Renamed %q to %q.\n", args[1], args[2])
		return 0

	default:
		log.Errorf("Unknown session command %q; try: list | show | rm | rename\n", args[0])
		return 1
	}
}

func sessionList(store session.Store, stdout io.Writer, log *tui.Logger) int {
	metas, err := store.List()
	if err != nil {
		log.Errorf("Error listing sessions: %v\n", err)
		return 1
	}
	if len(metas) == 0 {
		fmt.Fprintf(stdout, "No sessions yet (%s backend, %s).\n", session.Backend(), paths.DataDir())
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tROLE\tTURNS\tUPDATED\tCWD")
	for _, m := range metas {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", m.Name, m.Role, m.Turns, humanAge(m.UpdatedAt), m.Cwd)
	}
	return flush(tw)
}

func sessionShow(store session.Store, name string, stdout io.Writer, log *tui.Logger) int {
	sess, err := store.Get(name)
	if err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "name:    %s\nrole:    %s\nmodel:   %s\ncwd:     %s\ncreated: %s\nupdated: %s\nturns:   %d\n\n",
		sess.Meta.Name, sess.Meta.Role, sess.Meta.Model, sess.Meta.Cwd,
		sess.Meta.CreatedAt.Format(time.RFC3339), humanAge(sess.Meta.UpdatedAt), sess.Meta.Turns)

	// Show the tail of the transcript; full turns can be very long.
	const tail = 20
	turns := sess.Turns
	if len(turns) > tail {
		fmt.Fprintf(stdout, "… %d earlier turns omitted …\n", len(turns)-tail)
		turns = turns[len(turns)-tail:]
	}
	for i, t := range turns {
		fmt.Fprintf(stdout, "%4d  %-9s %-11s %s\n", len(sess.Turns)-len(turns)+i+1, t.Role, t.Kind, clip(t.Content, 100))
	}
	return 0
}

func flush(tw *tabwriter.Writer) int {
	if err := tw.Flush(); err != nil {
		return 1
	}
	return 0
}

// humanAge renders a timestamp as an age, e.g. "3m ago".
func humanAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// clip shortens s to at most n runes, collapsing newlines.
func clip(s string, n int) string {
	s = string([]rune(s))
	flat := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		flat = append(flat, r)
	}
	if len(flat) > n {
		return string(flat[:n]) + "…"
	}
	return string(flat)
}
