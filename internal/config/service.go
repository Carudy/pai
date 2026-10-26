package config

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Service manifests define a resident session and its host-side checks. The
// normalized form is what the CLI validates and what is stored as JSON in
// sessions.spec; the store remains a dumb blob.
const (
	// MinWatchInterval keeps a manifest from becoming a busy loop. It is a
	// structural rail, not a cost budget.
	MinWatchInterval = 10 * time.Second
	// MaxWatchersPerService bounds the probe pool a single service can create.
	MaxWatchersPerService = 8
	// ReportWatcherName is the reserved timer watcher that carries
	// service.report_every. Manifest watcher names must start with a letter, so a
	// user cannot collide with it.
	ReportWatcherName = "_report"
)

// ServiceManifest is one parsed, normalized manifest.
type ServiceManifest struct {
	Goal        string
	Role        string
	Workspace   string
	ReportEvery time.Duration
	OnChange    bool
	Watchers    []WatcherSpec
	// JSON is the canonical stored representation.
	JSON string
}

// WatcherSpec is one host-side probe.
type WatcherSpec struct {
	Name     string
	Kind     string // timer | command
	Cmd      string
	Every    time.Duration
	Timeout  time.Duration
	Debounce time.Duration // the manifest's "for"
	Cooldown time.Duration
	Severity string
	Trigger  *WatchTrigger
}

// WatchTrigger is the edge condition a command probe evaluates.
type WatchTrigger struct {
	Kind  string // json_number | json_string | json_bool | changed
	Field string
	Op    string
	Value any
}

type serviceFile struct {
	Service serviceSection `toml:"service"`
	Watch   []watchSection `toml:"watch"`
}

type serviceSection struct {
	Goal        string `toml:"goal"`
	Role        string `toml:"role"`
	Workspace   string `toml:"workspace"`
	ReportEvery string `toml:"report_every"`
	OnChange    bool   `toml:"on_change"`
}

type watchSection struct {
	Name     string          `toml:"name"`
	Kind     string          `toml:"kind"`
	Every    string          `toml:"every"`
	Timeout  string          `toml:"timeout"`
	Cmd      string          `toml:"cmd"`
	For      string          `toml:"for"`
	Cooldown string          `toml:"cooldown"`
	Severity string          `toml:"severity"`
	Trigger  *triggerSection `toml:"trigger"`
}

type triggerSection struct {
	Kind  string `toml:"kind"`
	Field string `toml:"field"`
	Op    string `toml:"op"`
	Value any    `toml:"value"`
}

// ParseServiceManifest parses and validates a manifest file.
func ParseServiceManifest(data []byte) (*ServiceManifest, error) {
	var raw serviceFile
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse service manifest: %w", err)
	}
	manifest, err := normalizeManifest(raw)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(manifestJSONOf(manifest))
	if err != nil {
		return nil, fmt.Errorf("encode service manifest: %w", err)
	}
	manifest.JSON = string(encoded)
	return manifest, nil
}

// NextWake returns the earliest heartbeat or watcher interval from now. A
// service with no report interval and no watchers waits for external events.
func (m *ServiceManifest) NextWake(now time.Time) (time.Time, bool) {
	var next time.Time
	found := false
	if m.ReportEvery > 0 {
		next = now.Add(m.ReportEvery)
		found = true
	}
	for _, w := range m.Watchers {
		if w.Every <= 0 {
			continue
		}
		at := now.Add(w.Every)
		if !found || at.Before(next) {
			next, found = at, true
		}
	}
	return next, found
}

// ParseStoredServiceSpec decodes the canonical JSON stored in sessions.spec.
// It does not re-run creation-time policy; the stored form was validated when
// the service was created.
func ParseStoredServiceSpec(spec string) (*ServiceManifest, error) {
	if strings.TrimSpace(spec) == "" || strings.TrimSpace(spec) == "{}" {
		return &ServiceManifest{}, nil
	}
	var raw manifestJSON
	if err := json.Unmarshal([]byte(spec), &raw); err != nil {
		return nil, fmt.Errorf("parse stored service spec: %w", err)
	}
	manifest := &ServiceManifest{
		Goal: raw.Goal, Role: raw.Role, Workspace: raw.Workspace,
		OnChange: raw.OnChange, JSON: spec,
	}
	var err error
	if manifest.ReportEvery, err = parseDuration(raw.ReportEvery); err != nil {
		return nil, fmt.Errorf("stored report_every: %w", err)
	}
	for _, w := range raw.Watch {
		spec, err := watcherFromJSON(w)
		if err != nil {
			return nil, fmt.Errorf("stored watcher %q: %w", w.Name, err)
		}
		manifest.Watchers = append(manifest.Watchers, spec)
	}
	return manifest, nil
}

// ParseWatcherSpec decodes one watcher's stored JSON. Like
// ParseStoredServiceSpec it parses only: the values were validated before they
// were stored, so this must not reject a row a running service already owns.
func ParseWatcherSpec(spec string) (WatcherSpec, error) {
	var raw watcherJSON
	if err := json.Unmarshal([]byte(spec), &raw); err != nil {
		return WatcherSpec{}, fmt.Errorf("parse watcher spec: %w", err)
	}
	return watcherFromJSON(raw)
}

func watcherFromJSON(raw watcherJSON) (WatcherSpec, error) {
	w := WatcherSpec{Name: raw.Name, Kind: raw.Kind, Cmd: raw.Cmd, Severity: raw.Severity}
	var err error
	if w.Every, err = parseDuration(raw.Every); err != nil {
		return WatcherSpec{}, fmt.Errorf("every: %w", err)
	}
	if w.Timeout, err = parseDuration(raw.Timeout); err != nil {
		return WatcherSpec{}, fmt.Errorf("timeout: %w", err)
	}
	if w.Debounce, err = parseDuration(raw.For); err != nil {
		return WatcherSpec{}, fmt.Errorf("for: %w", err)
	}
	if w.Cooldown, err = parseDuration(raw.Cooldown); err != nil {
		return WatcherSpec{}, fmt.Errorf("cooldown: %w", err)
	}
	if raw.Trigger != nil {
		w.Trigger = &WatchTrigger{Kind: raw.Trigger.Kind, Field: raw.Trigger.Field, Op: raw.Trigger.Op, Value: raw.Trigger.Value}
	}
	return w, nil
}

func normalizeManifest(raw serviceFile) (*ServiceManifest, error) {
	manifest := &ServiceManifest{
		Goal:      strings.TrimSpace(raw.Service.Goal),
		Role:      strings.TrimSpace(raw.Service.Role),
		Workspace: strings.TrimSpace(raw.Service.Workspace),
		OnChange:  raw.Service.OnChange,
	}
	if manifest.Goal == "" {
		return nil, fmt.Errorf("service.goal is required")
	}
	if len(manifest.Goal) > 4<<10 {
		return nil, fmt.Errorf("service.goal is longer than 4 KiB")
	}
	if manifest.Role == "" {
		return nil, fmt.Errorf("service.role is required")
	}
	if manifest.Workspace == "" {
		return nil, fmt.Errorf("service.workspace is required")
	}
	var err error
	if manifest.ReportEvery, err = parseDuration(raw.Service.ReportEvery); err != nil {
		return nil, fmt.Errorf("service.report_every: %w", err)
	}
	if manifest.ReportEvery > 0 && manifest.ReportEvery < MinWatchInterval {
		return nil, fmt.Errorf("service.report_every must be at least %s", MinWatchInterval)
	}
	if len(raw.Watch) > MaxWatchersPerService {
		return nil, fmt.Errorf("at most %d watchers are allowed", MaxWatchersPerService)
	}
	seen := make(map[string]bool, len(raw.Watch))
	for _, w := range raw.Watch {
		spec, err := normalizeWatcher(w)
		if err != nil {
			return nil, err
		}
		if seen[spec.Name] {
			return nil, fmt.Errorf("duplicate watcher name %q", spec.Name)
		}
		seen[spec.Name] = true
		manifest.Watchers = append(manifest.Watchers, spec)
	}
	return manifest, nil
}

var watcherNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,40}$`)

func normalizeWatcher(raw watchSection) (WatcherSpec, error) {
	w := WatcherSpec{
		Name:     strings.TrimSpace(raw.Name),
		Kind:     strings.ToLower(strings.TrimSpace(raw.Kind)),
		Cmd:      strings.TrimSpace(raw.Cmd),
		Severity: strings.ToLower(strings.TrimSpace(raw.Severity)),
	}
	if !watcherNameRe.MatchString(w.Name) {
		return WatcherSpec{}, fmt.Errorf("watcher name %q must match %s", w.Name, watcherNameRe)
	}
	if w.Kind != "timer" && w.Kind != "command" {
		return WatcherSpec{}, fmt.Errorf("watcher %q: kind must be timer or command", w.Name)
	}
	var err error
	if w.Every, err = parseDuration(raw.Every); err != nil {
		return WatcherSpec{}, fmt.Errorf("watcher %q: every: %w", w.Name, err)
	}
	if w.Every < MinWatchInterval {
		return WatcherSpec{}, fmt.Errorf("watcher %q: every must be at least %s", w.Name, MinWatchInterval)
	}
	if w.Every > 24*time.Hour {
		return WatcherSpec{}, fmt.Errorf("watcher %q: every is longer than 24h", w.Name)
	}
	if w.Timeout, err = parseDuration(raw.Timeout); err != nil {
		return WatcherSpec{}, fmt.Errorf("watcher %q: timeout: %w", w.Name, err)
	}
	if w.Timeout == 0 {
		w.Timeout = min(10*time.Second, w.Every)
	}
	if w.Timeout > w.Every {
		return WatcherSpec{}, fmt.Errorf("watcher %q: timeout must not exceed every", w.Name)
	}
	if w.Debounce, err = parseDuration(raw.For); err != nil {
		return WatcherSpec{}, fmt.Errorf("watcher %q: for: %w", w.Name, err)
	}
	if w.Cooldown, err = parseDuration(raw.Cooldown); err != nil {
		return WatcherSpec{}, fmt.Errorf("watcher %q: cooldown: %w", w.Name, err)
	}
	if w.Severity == "" {
		w.Severity = "info"
	}
	switch w.Severity {
	case "info", "low", "medium", "high", "critical":
	default:
		return WatcherSpec{}, fmt.Errorf("watcher %q: unknown severity %q", w.Name, w.Severity)
	}
	if w.Kind == "timer" {
		if w.Cmd != "" || raw.Trigger != nil {
			return WatcherSpec{}, fmt.Errorf("watcher %q: a timer watcher takes no cmd or trigger", w.Name)
		}
		return w, nil
	}
	if w.Cmd == "" {
		return WatcherSpec{}, fmt.Errorf("watcher %q: command is required", w.Name)
	}
	if len(w.Cmd) > 4<<10 {
		return WatcherSpec{}, fmt.Errorf("watcher %q: command is longer than 4 KiB", w.Name)
	}
	if raw.Trigger == nil {
		return WatcherSpec{}, fmt.Errorf("watcher %q: command watchers need a trigger", w.Name)
	}
	trig, err := normalizeTrigger(w.Name, *raw.Trigger)
	if err != nil {
		return WatcherSpec{}, err
	}
	w.Trigger = trig
	return w, nil
}

func normalizeTrigger(watcher string, raw triggerSection) (*WatchTrigger, error) {
	t := &WatchTrigger{
		Kind: strings.ToLower(strings.TrimSpace(raw.Kind)), Field: strings.TrimSpace(raw.Field),
		Op: strings.TrimSpace(raw.Op), Value: raw.Value,
	}
	switch t.Kind {
	case "changed":
		return t, nil
	case "json_number", "json_string", "json_bool":
	default:
		return nil, fmt.Errorf("watcher %q: trigger kind must be json_number, json_string, json_bool, or changed", watcher)
	}
	if t.Field == "" {
		return nil, fmt.Errorf("watcher %q: trigger.field is required", watcher)
	}
	if t.Value == nil {
		return nil, fmt.Errorf("watcher %q: trigger.value is required", watcher)
	}
	valid := false
	switch t.Kind {
	case "json_number":
		switch t.Value.(type) {
		case int64, float64:
			valid = t.Op == ">" || t.Op == ">=" || t.Op == "<" || t.Op == "<=" || t.Op == "==" || t.Op == "!="
		}
	case "json_string":
		switch t.Value.(type) {
		case string:
			valid = t.Op == "==" || t.Op == "!=" || t.Op == "contains"
		}
	case "json_bool":
		if _, ok := t.Value.(bool); ok {
			valid = t.Op == "==" || t.Op == "!="
		}
	}
	if !valid {
		return nil, fmt.Errorf("watcher %q: trigger value/op do not match kind %s", watcher, t.Kind)
	}
	return t, nil
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	return time.ParseDuration(s)
}

func durationString(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
}

type manifestJSON struct {
	Goal        string        `json:"goal"`
	Role        string        `json:"role"`
	Workspace   string        `json:"workspace"`
	ReportEvery string        `json:"report_every,omitempty"`
	OnChange    bool          `json:"on_change,omitempty"`
	Watch       []watcherJSON `json:"watch,omitempty"`
}

type watcherJSON struct {
	Name     string       `json:"name"`
	Kind     string       `json:"kind"`
	Every    string       `json:"every"`
	Timeout  string       `json:"timeout,omitempty"`
	Cmd      string       `json:"cmd,omitempty"`
	For      string       `json:"for,omitempty"`
	Cooldown string       `json:"cooldown,omitempty"`
	Severity string       `json:"severity,omitempty"`
	Trigger  *triggerJSON `json:"trigger,omitempty"`
}

type triggerJSON struct {
	Kind  string `json:"kind"`
	Field string `json:"field,omitempty"`
	Op    string `json:"op,omitempty"`
	Value any    `json:"value,omitempty"`
}

func manifestJSONOf(m *ServiceManifest) manifestJSON {
	out := manifestJSON{
		Goal: m.Goal, Role: m.Role, Workspace: m.Workspace,
		ReportEvery: durationString(m.ReportEvery), OnChange: m.OnChange,
	}
	for _, w := range m.Watchers {
		out.Watch = append(out.Watch, watcherJSONOf(w))
	}
	return out
}

// MarshalWatcherSpec renders one watcher's canonical JSON, which is what the
// session store keeps in watchers.spec.
func MarshalWatcherSpec(w WatcherSpec) string {
	encoded, err := json.Marshal(watcherJSONOf(w))
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func watcherJSONOf(w WatcherSpec) watcherJSON {
	jw := watcherJSON{
		Name: w.Name, Kind: w.Kind, Every: durationString(w.Every),
		Timeout: durationString(w.Timeout), Cmd: w.Cmd, For: durationString(w.Debounce),
		Cooldown: durationString(w.Cooldown), Severity: w.Severity,
	}
	if w.Trigger != nil {
		jw.Trigger = &triggerJSON{Kind: w.Trigger.Kind, Field: w.Trigger.Field, Op: w.Trigger.Op, Value: w.Trigger.Value}
	}
	return jw
}
