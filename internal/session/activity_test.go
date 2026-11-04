package session

import (
	"strings"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/core"
)

// A group timeline is built from two shapes that are keyed differently, so each
// read has to be faithful on its own: events by immutable id (filtered to what a
// group actually exchanges) and reported outputs by display name.
func TestGroupActivitySources(t *testing.T) {
	s := openSQLiteStore(t)
	coord := makeService(t, s, "coord")
	hydro := makeService(t, s, "hydro")
	now := time.Now()

	appendEv := func(sessionID, kind, source, payload string, at time.Time) {
		t.Helper()
		if _, err := s.AppendEvent(core.Event{
			Session: sessionID, Class: core.EventPeer, Kind: kind,
			Source: source, Payload: payload, At: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendEv(hydro, "mail", "session:coord", "gate schedule final?", now.Add(-2*time.Minute))
	appendEv(hydro, "user_input", "user", "check the gate", now.Add(-time.Minute))
	// A probe is a member's own business; the timeline must not show it.
	appendEv(hydro, "watch", "watch:level", "1 level=4.4", now)

	events, err := s.GroupEvents([]string{coord, hydro}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want the mail and the instruction only", events)
	}
	if events[0].Kind != "user_input" || events[1].Kind != "mail" {
		t.Fatalf("events are not newest first: %+v", events)
	}
	if events[0].State != "pending" {
		t.Fatalf("state = %q, want the delivery state filled in", events[0].State)
	}

	if err := s.Append("hydro",
		core.Turn{Role: "assistant", Kind: core.KindOutput, Content: `{"action":"inform","payload":"level 3.8 m"}`},
		core.Turn{Role: "assistant", Kind: core.KindToolResult, Content: "command output"},
	); err != nil {
		t.Fatal(err)
	}
	turns, err := s.RecentOutputs([]string{"coord", "hydro"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Name != "hydro" || !strings.Contains(turns[0].Turn.Content, "inform") {
		t.Fatalf("outputs = %+v, want the single assistant output of hydro", turns)
	}
	if turns[0].Turn.At.IsZero() {
		t.Fatal("the turn has no timestamp")
	}

	// An empty member set must be a cheap no-op, not a malformed IN ().
	if events, err := s.GroupEvents(nil, nil, 10); err != nil || len(events) != 0 {
		t.Fatalf("empty group = %+v, %v", events, err)
	}
	if turns, err := s.RecentOutputs(nil, 10); err != nil || len(turns) != 0 {
		t.Fatalf("empty names = %+v, %v", turns, err)
	}
}
