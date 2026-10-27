package config

import (
	"strings"
	"testing"
	"time"
)

const validManifest = `
[service]
goal      = "monitor river level and gate state"
role      = "devops"
workspace = "/srv/hydro"
report_interval = "1h"
report_on_change = true

[[watcher]]
name    = "river-level"
type    = "command"
interval = "60s"
timeout = "10s"
command = "hydroctl level"
hold_for = "2m"
repeat_after = "15m"
severity = "high"

[[watcher]]
name  = "heartbeat"
type  = "timer"
interval = "30s"
prompt = "Report the current state in one line."
`

func TestParseServiceManifest(t *testing.T) {
	m, err := ParseServiceManifest([]byte(validManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Goal == "" || m.Role != "devops" || m.Workspace != "/srv/hydro" {
		t.Fatalf("manifest = %+v", m)
	}
	if m.ReportInterval != time.Hour || !m.ReportOnChange {
		t.Fatalf("service fields = %+v", m)
	}
	if len(m.Watchers) != 2 {
		t.Fatalf("watchers = %+v", m.Watchers)
	}
	w := m.Watchers[0]
	if w.Name != "river-level" || w.Type != "command" || w.Interval != time.Minute || w.Timeout != 10*time.Second {
		t.Fatalf("watcher = %+v", w)
	}
	if w.HoldFor != 2*time.Minute || w.RepeatAfter != 15*time.Minute || w.Severity != "high" {
		t.Fatalf("watcher timing = %+v", w)
	}
	if w.Command != "hydroctl level" {
		t.Fatalf("command = %q", w.Command)
	}
	if m.Watchers[1].Timeout != 10*time.Second {
		t.Fatalf("default timeout = %v, want 10s", m.Watchers[1].Timeout)
	}
	if m.Watchers[1].Prompt != "Report the current state in one line." {
		t.Fatalf("timer prompt = %q", m.Watchers[1].Prompt)
	}
	if m.JSON == "" || !strings.Contains(m.JSON, `"report_interval":"1h0m0s"`) ||
		!strings.Contains(m.JSON, `"watchers":[`) {
		t.Fatalf("canonical json = %s", m.JSON)
	}

	stored, err := ParseStoredServiceSpec(m.JSON)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ReportInterval != time.Hour || len(stored.Watchers) != 2 ||
		stored.Watchers[0].Interval != time.Minute {
		t.Fatalf("stored manifest = %+v", stored)
	}
	if stored.Watchers[0].HoldFor != 2*time.Minute || stored.Watchers[0].RepeatAfter != 15*time.Minute {
		t.Fatalf("stored watcher timing = %+v", stored.Watchers[0])
	}
}

func TestServiceManifestRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		edit func(string) string
		want string
	}{
		{"missing goal", func(s string) string {
			return strings.Replace(s, `goal      = "monitor river level and gate state"`, `goal = ""`, 1)
		}, "goal is required"},
		{"missing role", func(s string) string { return strings.Replace(s, `role      = "devops"`, `role = ""`, 1) }, "role is required"},
		{"missing workspace", func(s string) string { return strings.Replace(s, `workspace = "/srv/hydro"`, `workspace = ""`, 1) }, "workspace is required"},
		{"short report", func(s string) string { return strings.Replace(s, `"1h"`, `"1s"`, 1) }, "at least 10s"},
		{"short interval", func(s string) string { return strings.Replace(s, `interval = "60s"`, `interval = "1s"`, 1) }, "interval must be at least"},
		{"long interval", func(s string) string { return strings.Replace(s, `interval = "60s"`, `interval = "48h"`, 1) }, "longer than 24h"},
		{"bad timeout", func(s string) string { return strings.Replace(s, `timeout = "10s"`, `timeout = "2m"`, 1) }, "timeout must not exceed"},
		{"timer command", func(s string) string {
			return strings.Replace(s, `interval = "30s"`, "interval = \"30s\"\ncommand = \"true\"", 1)
		}, "takes no command"},
		{"command without command", func(s string) string {
			return strings.Replace(s, `command = "hydroctl level"`, ``, 1)
		}, "command is required"},
		{"bad type", func(s string) string {
			return strings.Replace(s, `type    = "command"`, `type = "cron"`, 1)
		}, "type must be timer or command"},
		{"bad hold_for", func(s string) string {
			return strings.Replace(s, `hold_for = "2m"`, `hold_for = "soon"`, 1)
		}, "hold_for"},
		{"bad repeat_after", func(s string) string {
			return strings.Replace(s, `repeat_after = "15m"`, `repeat_after = "later"`, 1)
		}, "repeat_after"},
		{"command prompt", func(s string) string {
			return strings.Replace(s, `severity = "high"`, "severity = \"high\"\nprompt = \"run it\"", 1)
		}, "takes no prompt"},
		{"bad severity", func(s string) string { return strings.Replace(s, `severity = "high"`, `severity = "urgent"`, 1) }, "unknown severity"},
		{"duplicate watcher", func(s string) string {
			return s + "\n[[watcher]]\nname = \"river-level\"\ntype = \"timer\"\ninterval = \"30s\"\n"
		}, "duplicate watcher"},
		{"bad name", func(s string) string { return strings.Replace(s, `name    = "river-level"`, `name = "River Level"`, 1) }, "must match"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseServiceManifest([]byte(tc.edit(validManifest)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestServiceManifestWatcherLimit(t *testing.T) {
	body := `
[service]
goal = "g"
role = "devops"
workspace = "/w"
`
	for i := 0; i < MaxWatchersPerService+1; i++ {
		body += "\n[[watcher]]\nname = \"w" + string(rune('a'+i)) + "\"\ntype = \"timer\"\ninterval = \"30s\"\n"
	}
	_, err := ParseServiceManifest([]byte(body))
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("err = %v, want the watcher limit", err)
	}
}

func TestRenderServiceManifestTOMLRoundTrip(t *testing.T) {
	m, err := ParseServiceManifest([]byte(validManifest))
	if err != nil {
		t.Fatal(err)
	}
	text, err := RenderServiceManifestTOML(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	back, err := ParseServiceManifest([]byte(text))
	if err != nil {
		t.Fatalf("re-parse rendered manifest: %v\n%s", err, text)
	}
	if back.Goal != m.Goal || back.Role != m.Role || back.Workspace != m.Workspace ||
		back.ReportInterval != m.ReportInterval || back.ReportOnChange != m.ReportOnChange {
		t.Fatalf("service fields = %+v, want %+v", back, m)
	}
	if len(back.Watchers) != len(m.Watchers) {
		t.Fatalf("watchers = %d, want %d\n%s", len(back.Watchers), len(m.Watchers), text)
	}
	for i := range m.Watchers {
		if back.Watchers[i] != m.Watchers[i] {
			t.Fatalf("watcher %d = %+v, want %+v", i, back.Watchers[i], m.Watchers[i])
		}
	}
}

func TestWatcherSpecRoundTrip(t *testing.T) {
	w := WatcherSpec{
		Name: "river-level", Type: "command", Command: `cat /srv/hydro/level.json`,
		Interval: time.Minute, Timeout: 5 * time.Second,
		HoldFor: 2 * time.Minute, RepeatAfter: time.Hour, Severity: "high",
	}
	back, err := ParseWatcherSpec(MarshalWatcherSpec(w))
	if err != nil {
		t.Fatalf("ParseWatcherSpec: %v", err)
	}
	if back.Name != w.Name || back.Type != w.Type || back.Command != w.Command ||
		back.Interval != w.Interval || back.Timeout != w.Timeout || back.HoldFor != w.HoldFor ||
		back.RepeatAfter != w.RepeatAfter || back.Severity != w.Severity {
		t.Fatalf("round trip = %+v, want %+v", back, w)
	}

	tick := WatcherSpec{
		Name: "tick", Type: "timer", Interval: 5 * time.Minute,
		Prompt: "Report the current state in one line.", Severity: "info",
	}
	back, err = ParseWatcherSpec(MarshalWatcherSpec(tick))
	if err != nil {
		t.Fatalf("ParseWatcherSpec(timer): %v", err)
	}
	if back.Prompt != tick.Prompt {
		t.Fatalf("timer prompt = %q, want %q", back.Prompt, tick.Prompt)
	}
}
