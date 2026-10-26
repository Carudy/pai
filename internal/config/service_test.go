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
report_every = "1h"
on_change = true

[[watch]]
name    = "river-level"
kind    = "command"
every   = "60s"
timeout = "10s"
cmd     = "hydroctl level --json"
trigger = { kind = "json_number", field = "level", op = ">", value = 3.2 }
for     = "2m"
cooldown = "15m"
severity = "high"

[[watch]]
name  = "heartbeat"
kind  = "timer"
every = "30s"
`

func TestParseServiceManifest(t *testing.T) {
	m, err := ParseServiceManifest([]byte(validManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Goal == "" || m.Role != "devops" || m.Workspace != "/srv/hydro" {
		t.Fatalf("manifest = %+v", m)
	}
	if m.ReportEvery != time.Hour || !m.OnChange {
		t.Fatalf("service fields = %+v", m)
	}
	if len(m.Watchers) != 2 {
		t.Fatalf("watchers = %+v", m.Watchers)
	}
	w := m.Watchers[0]
	if w.Name != "river-level" || w.Kind != "command" || w.Every != time.Minute || w.Timeout != 10*time.Second {
		t.Fatalf("watcher = %+v", w)
	}
	if w.Debounce != 2*time.Minute || w.Cooldown != 15*time.Minute || w.Severity != "high" {
		t.Fatalf("watcher timing = %+v", w)
	}
	if w.Trigger == nil || w.Trigger.Kind != "json_number" || w.Trigger.Field != "level" || w.Trigger.Op != ">" {
		t.Fatalf("trigger = %+v", w.Trigger)
	}
	if w.Trigger.Value != 3.2 {
		t.Fatalf("trigger value = %#v", w.Trigger.Value)
	}
	if m.Watchers[1].Timeout != 10*time.Second {
		t.Fatalf("default timeout = %v, want 10s", m.Watchers[1].Timeout)
	}
	if m.JSON == "" || !strings.Contains(m.JSON, `"report_every":"1h0m0s"`) {
		t.Fatalf("canonical json = %s", m.JSON)
	}

	stored, err := ParseStoredServiceSpec(m.JSON)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ReportEvery != time.Hour || len(stored.Watchers) != 2 || stored.Watchers[0].Every != time.Minute {
		t.Fatalf("stored manifest = %+v", stored)
	}
	if stored.Watchers[0].Trigger == nil || stored.Watchers[0].Trigger.Value != 3.2 {
		t.Fatalf("stored trigger = %+v", stored.Watchers[0].Trigger)
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
		{"short every", func(s string) string { return strings.Replace(s, `every   = "60s"`, `every = "1s"`, 1) }, "every must be at least"},
		{"bad timeout", func(s string) string { return strings.Replace(s, `timeout = "10s"`, `timeout = "2m"`, 1) }, "timeout must not exceed"},
		{"timer cmd", func(s string) string {
			return strings.Replace(s, `every = "30s"`, "every = \"30s\"\ncmd = \"true\"", 1)
		}, "takes no cmd"},
		{"command without trigger", func(s string) string {
			return strings.Replace(s, `trigger = { kind = "json_number", field = "level", op = ">", value = 3.2 }`, ``, 1)
		}, "need a trigger"},
		{"bad trigger op", func(s string) string {
			return strings.Replace(s, `op = ">"`, `op = "contains"`, 1)
		}, "value/op do not match"},
		{"bad trigger kind", func(s string) string {
			return strings.Replace(s, `kind = "json_number"`, `kind = "regex"`, 1)
		}, "trigger kind"},
		{"bad severity", func(s string) string { return strings.Replace(s, `severity = "high"`, `severity = "urgent"`, 1) }, "unknown severity"},
		{"duplicate watcher", func(s string) string {
			return s + "\n[[watch]]\nname = \"river-level\"\nkind = \"timer\"\nevery = \"30s\"\n"
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
		body += "\n[[watch]]\nname = \"w" + string(rune('a'+i)) + "\"\nkind = \"timer\"\nevery = \"30s\"\n"
	}
	_, err := ParseServiceManifest([]byte(body))
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("err = %v, want the watcher limit", err)
	}
}
