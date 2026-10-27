package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/config"
)

// drainDiagnostics reads what a probe pass produced. Activations are
// synchronous, so by the time Activate returns every line is already buffered.
func drainDiagnostics(ch <-chan Diagnostic) []Diagnostic {
	var out []Diagnostic
	for {
		select {
		case d := <-ch:
			out = append(out, d)
		default:
			return out
		}
	}
}

func findDiagnostic(list []Diagnostic, match func(Diagnostic) bool) *Diagnostic {
	for i := range list {
		if match(list[i]) {
			return &list[i]
		}
	}
	return nil
}

// Every probe reports its verdict, fired or not, and the ways a probe can stay
// quiet are named rather than silent.
func TestProbeDiagnosticsCoverEveryVerdict(t *testing.T) {
	skipWithoutPOSIXShell(t)
	past := time.Now().Add(-time.Minute)
	fire := config.WatcherSpec{Name: "river", Command: `printf '1 level=9\n'`, Interval: time.Minute, Timeout: 5 * time.Second}
	quiet := config.WatcherSpec{Name: "tide", Command: "echo 0", Interval: time.Minute, Timeout: 5 * time.Second}
	latched := config.WatcherSpec{Name: "latched", Command: "echo 1", Interval: time.Minute, Timeout: 5 * time.Second}
	fp := &serviceTestProvider{
		replies: []string{`{"action":"done","payload":"reported","reason":"done"}`},
		done:    make(chan int, 2),
	}
	m, _ := newWatchService(t, fp,
		WatcherState{ID: "w1", SessionID: "sid-w", Name: fire.Name, Spec: config.MarshalWatcherSpec(fire), Enabled: true, ApprovedAt: &past, NextRun: &past},
		WatcherState{ID: "w2", SessionID: "sid-w", Name: quiet.Name, Spec: config.MarshalWatcherSpec(quiet), Enabled: true, ApprovedAt: &past, NextRun: &past},
		// Already reported in this episode, so the latch suppresses the wake.
		WatcherState{ID: "w4", SessionID: "sid-w", Name: latched.Name, Spec: config.MarshalWatcherSpec(latched), Enabled: true, ApprovedAt: &past, NextRun: &past, LastState: `{"fired":1,"reported":true}`},
	)
	diagnostics, unsubscribe, err := m.SubscribeDiagnostics()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	activities, unsubscribeActivities, err := m.SubscribeActivities()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeActivities()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := m.Activate(ctx, "svc", clockWake()); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	got := drainDiagnostics(diagnostics)

	fired := findDiagnostic(got, func(d Diagnostic) bool { return d.Subject == "river" })
	if fired == nil || fired.Kind != "watcher" || fired.Outcome != "fired" {
		t.Fatalf("no fired verdict in %+v", got)
	}
	if !strings.Contains(fired.Detail, "level=9") || fired.Next.IsZero() || fired.Session != "svc" {
		t.Fatalf("fired diagnostic = %+v", fired)
	}
	quietDiag := findDiagnostic(got, func(d Diagnostic) bool { return d.Subject == "tide" })
	if quietDiag == nil || quietDiag.Outcome != "quiet" || quietDiag.Detail != "0" {
		t.Fatalf("a quiet probe must still report its result: %+v", quietDiag)
	}
	held := findDiagnostic(got, func(d Diagnostic) bool { return d.Subject == "latched" })
	if held == nil || held.Outcome != "held" || held.Reason != "already reported this episode" {
		t.Fatalf("a latched probe must say why it stayed quiet: %+v", held)
	}
	if held.Detail != "1" {
		t.Fatalf("held diagnostic detail = %q, want the raw decision", held.Detail)
	}
	if findDiagnostic(got, func(d Diagnostic) bool { return d.Kind == "activation" && d.Outcome == "start" }) == nil {
		t.Fatalf("no activation start in %+v", got)
	}
	if findDiagnostic(got, func(d Diagnostic) bool { return d.Kind == "activation" && d.Outcome == "done" }) == nil {
		t.Fatalf("no activation outcome in %+v", got)
	}

	// Verdicts repeat on every interval, so they stay out of the browser stream.
	for {
		select {
		case a := <-activities:
			if a.Event.Type == "watch" || a.Event.Type == "activation" {
				t.Fatalf("probe diagnostics leaked into activities: %+v", a)
			}
		default:
			return
		}
	}
}

// Errors and notices already exist as events; mirroring them is what makes the
// diagnostics log useful when a provider stalls or a write fails.
func TestErrorEventsReachDiagnostics(t *testing.T) {
	m := New(context.Background(), &fakeBackend{}, 1)
	defer m.Close()
	ch, unsubscribe, err := m.SubscribeDiagnostics()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	w := &worker{m: m, name: "one"}
	w.emit("error", "boom")
	w.emit("notice", "retrying")

	got := drainDiagnostics(ch)
	if findDiagnostic(got, func(d Diagnostic) bool { return d.Kind == "error" && d.Detail == "boom" && d.Session == "one" }) == nil {
		t.Fatalf("error not mirrored: %+v", got)
	}
	if findDiagnostic(got, func(d Diagnostic) bool { return d.Kind == "notice" && d.Detail == "retrying" }) == nil {
		t.Fatalf("notice not mirrored: %+v", got)
	}
}

func TestDiagnosticsCloseWithManager(t *testing.T) {
	m := New(context.Background(), &fakeBackend{}, 1)
	ch, unsubscribe, err := m.SubscribeDiagnostics()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	m.Close()
	if _, ok := <-ch; ok {
		t.Fatal("diagnostics channel stayed open after Close")
	}
}
