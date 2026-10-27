package config

import (
	"bytes"
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
	// service.report_interval. Manifest watcher names must start with a letter,
	// so a user cannot collide with it.
	ReportWatcherName = "_report"
	// ReportPrompt is the reserved heartbeat's instruction: it is what the model
	// reads when the report interval comes due.
	ReportPrompt = "Report what changed since the last report. If nothing changed, yield."
	// maxCommand bounds a probe's command text. The command is stored in the
	// session row and shown in approval and inspection output.
	maxCommand = 4 << 10
	// maxPrompt bounds a timer's instruction, which is injected into the wake
	// on every tick.
	maxPrompt = 2 << 10
)

// ServiceManifest is one parsed, normalized manifest.
type ServiceManifest struct {
	Goal           string
	Role           string
	Workspace      string
	ReportInterval time.Duration
	ReportOnChange bool
	Watchers       []WatcherSpec
	// JSON is the canonical stored representation.
	JSON string
}

// WatcherSpec is one host-side check. A command watcher is a predicate, not a
// sensor: it prints 1/0 (or true/false) and everything after the decision token
// is the summary handed to the model. Keeping the decision in the command means
// the manifest owns no query language, and the probe can use whichever shell,
// Python, or jq is already on the host.
type WatcherSpec struct {
	Name        string
	Type        string // timer | command
	Command     string
	Prompt      string // timer only: what the model should do on each tick
	Interval    time.Duration
	Timeout     time.Duration
	HoldFor     time.Duration // the predicate must hold this long before firing
	RepeatAfter time.Duration // re-alert interval while it keeps holding; 0 = once per episode
	Severity    string
}

type serviceFile struct {
	Service  serviceSection `toml:"service"`
	Watchers []watchSection `toml:"watcher"`
}

type serviceSection struct {
	Goal           string `toml:"goal"`
	Role           string `toml:"role"`
	Workspace      string `toml:"workspace"`
	ReportInterval string `toml:"report_interval,omitempty"`
	ReportOnChange bool   `toml:"report_on_change,omitempty"`
}

type watchSection struct {
	Name        string `toml:"name"`
	Type        string `toml:"type"`
	Interval    string `toml:"interval"`
	Timeout     string `toml:"timeout,omitempty"`
	Command     string `toml:"command,omitempty"`
	Prompt      string `toml:"prompt,omitempty"`
	HoldFor     string `toml:"hold_for,omitempty"`
	RepeatAfter string `toml:"repeat_after,omitempty"`
	Severity    string `toml:"severity,omitempty"`
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
	encoded, err := MarshalServiceManifest(manifest)
	if err != nil {
		return nil, err
	}
	manifest.JSON = encoded
	return manifest, nil
}

// MarshalServiceManifest renders a manifest's canonical stored JSON.
func MarshalServiceManifest(m *ServiceManifest) (string, error) {
	encoded, err := json.Marshal(manifestJSONOf(m))
	if err != nil {
		return "", fmt.Errorf("encode service manifest: %w", err)
	}
	return string(encoded), nil
}

// RenderServiceManifestTOML renders an editable TOML document. It is the inverse
// of ParseServiceManifest and exists so an editor can prefill the exact form the
// manifest was written in; the canonical stored form remains JSON.
func RenderServiceManifestTOML(m *ServiceManifest) (string, error) {
	raw := serviceFile{Service: serviceSection{
		Goal:           m.Goal,
		Role:           m.Role,
		Workspace:      m.Workspace,
		ReportInterval: durationString(m.ReportInterval),
		ReportOnChange: m.ReportOnChange,
	}}
	for _, w := range m.Watchers {
		raw.Watchers = append(raw.Watchers, watchSection{
			Name: w.Name, Type: w.Type, Interval: durationString(w.Interval),
			Timeout: durationString(w.Timeout), Command: w.Command, Prompt: w.Prompt,
			HoldFor: durationString(w.HoldFor), RepeatAfter: durationString(w.RepeatAfter),
			Severity: w.Severity,
		})
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(raw); err != nil {
		return "", fmt.Errorf("encode service manifest as TOML: %w", err)
	}
	return buf.String(), nil
}

// NextWake returns the earliest heartbeat or watcher interval from now. A
// service with no report interval and no watchers waits for external events.
func (m *ServiceManifest) NextWake(now time.Time) (time.Time, bool) {
	var next time.Time
	found := false
	if m.ReportInterval > 0 {
		next = now.Add(m.ReportInterval)
		found = true
	}
	for _, w := range m.Watchers {
		if w.Interval <= 0 {
			continue
		}
		at := now.Add(w.Interval)
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
		ReportOnChange: raw.ReportOnChange, JSON: spec,
	}
	var err error
	if manifest.ReportInterval, err = parseDuration(raw.ReportInterval); err != nil {
		return nil, fmt.Errorf("stored report_interval: %w", err)
	}
	for _, w := range raw.Watchers {
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
	w := WatcherSpec{Name: raw.Name, Type: raw.Type, Command: raw.Command, Prompt: raw.Prompt, Severity: raw.Severity}
	var err error
	if w.Interval, err = parseDuration(raw.Interval); err != nil {
		return WatcherSpec{}, fmt.Errorf("interval: %w", err)
	}
	if w.Timeout, err = parseDuration(raw.Timeout); err != nil {
		return WatcherSpec{}, fmt.Errorf("timeout: %w", err)
	}
	if w.HoldFor, err = parseDuration(raw.HoldFor); err != nil {
		return WatcherSpec{}, fmt.Errorf("hold_for: %w", err)
	}
	if w.RepeatAfter, err = parseDuration(raw.RepeatAfter); err != nil {
		return WatcherSpec{}, fmt.Errorf("repeat_after: %w", err)
	}
	return w, nil
}

func normalizeManifest(raw serviceFile) (*ServiceManifest, error) {
	manifest := &ServiceManifest{
		Goal:           strings.TrimSpace(raw.Service.Goal),
		Role:           strings.TrimSpace(raw.Service.Role),
		Workspace:      strings.TrimSpace(raw.Service.Workspace),
		ReportOnChange: raw.Service.ReportOnChange,
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
	if manifest.ReportInterval, err = parseDuration(raw.Service.ReportInterval); err != nil {
		return nil, fmt.Errorf("service.report_interval: %w", err)
	}
	if manifest.ReportInterval > 0 && manifest.ReportInterval < MinWatchInterval {
		return nil, fmt.Errorf("service.report_interval must be at least %s", MinWatchInterval)
	}
	if len(raw.Watchers) > MaxWatchersPerService {
		return nil, fmt.Errorf("at most %d watchers are allowed", MaxWatchersPerService)
	}
	seen := make(map[string]bool, len(raw.Watchers))
	for _, w := range raw.Watchers {
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
		Type:     strings.ToLower(strings.TrimSpace(raw.Type)),
		Command:  strings.TrimSpace(raw.Command),
		Prompt:   strings.TrimSpace(raw.Prompt),
		Severity: strings.ToLower(strings.TrimSpace(raw.Severity)),
	}
	if !watcherNameRe.MatchString(w.Name) {
		return WatcherSpec{}, fmt.Errorf("watcher name %q must match %s", w.Name, watcherNameRe)
	}
	if w.Type != "timer" && w.Type != "command" {
		return WatcherSpec{}, fmt.Errorf("watcher %q: type must be timer or command", w.Name)
	}
	var err error
	if w.Interval, err = parseDuration(raw.Interval); err != nil {
		return WatcherSpec{}, fmt.Errorf("watcher %q: interval: %w", w.Name, err)
	}
	if w.Interval < MinWatchInterval {
		return WatcherSpec{}, fmt.Errorf("watcher %q: interval must be at least %s", w.Name, MinWatchInterval)
	}
	if w.Interval > 24*time.Hour {
		return WatcherSpec{}, fmt.Errorf("watcher %q: interval is longer than 24h", w.Name)
	}
	if w.Timeout, err = parseDuration(raw.Timeout); err != nil {
		return WatcherSpec{}, fmt.Errorf("watcher %q: timeout: %w", w.Name, err)
	}
	if w.Timeout == 0 {
		w.Timeout = min(10*time.Second, w.Interval)
	}
	if w.Timeout > w.Interval {
		return WatcherSpec{}, fmt.Errorf("watcher %q: timeout must not exceed interval", w.Name)
	}
	if w.HoldFor, err = parseDuration(raw.HoldFor); err != nil {
		return WatcherSpec{}, fmt.Errorf("watcher %q: hold_for: %w", w.Name, err)
	}
	if w.RepeatAfter, err = parseDuration(raw.RepeatAfter); err != nil {
		return WatcherSpec{}, fmt.Errorf("watcher %q: repeat_after: %w", w.Name, err)
	}
	if w.Severity == "" {
		w.Severity = "info"
	}
	switch w.Severity {
	case "info", "low", "medium", "high", "critical":
	default:
		return WatcherSpec{}, fmt.Errorf("watcher %q: unknown severity %q", w.Name, w.Severity)
	}
	if w.Type == "timer" {
		if w.Command != "" {
			return WatcherSpec{}, fmt.Errorf("watcher %q: a timer takes no command", w.Name)
		}
		if len(w.Prompt) > maxPrompt {
			return WatcherSpec{}, fmt.Errorf("watcher %q: prompt is longer than 2 KiB", w.Name)
		}
		return w, nil
	}
	if w.Prompt != "" {
		return WatcherSpec{}, fmt.Errorf("watcher %q: a command watcher takes no prompt; its output is the summary", w.Name)
	}
	if w.Command == "" {
		return WatcherSpec{}, fmt.Errorf("watcher %q: command is required", w.Name)
	}
	if len(w.Command) > maxCommand {
		return WatcherSpec{}, fmt.Errorf("watcher %q: command is longer than 4 KiB", w.Name)
	}
	return w, nil
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
	Goal           string        `json:"goal"`
	Role           string        `json:"role"`
	Workspace      string        `json:"workspace"`
	ReportInterval string        `json:"report_interval,omitempty"`
	ReportOnChange bool          `json:"report_on_change,omitempty"`
	Watchers       []watcherJSON `json:"watchers,omitempty"`
}

type watcherJSON struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Interval    string `json:"interval"`
	Timeout     string `json:"timeout,omitempty"`
	Command     string `json:"command,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	HoldFor     string `json:"hold_for,omitempty"`
	RepeatAfter string `json:"repeat_after,omitempty"`
	Severity    string `json:"severity,omitempty"`
}

func manifestJSONOf(m *ServiceManifest) manifestJSON {
	out := manifestJSON{
		Goal: m.Goal, Role: m.Role, Workspace: m.Workspace,
		ReportInterval: durationString(m.ReportInterval), ReportOnChange: m.ReportOnChange,
	}
	for _, w := range m.Watchers {
		out.Watchers = append(out.Watchers, watcherJSONOf(w))
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
	return watcherJSON{
		Name: w.Name, Type: w.Type, Interval: durationString(w.Interval),
		Timeout: durationString(w.Timeout), Command: w.Command, Prompt: w.Prompt,
		HoldFor: durationString(w.HoldFor), RepeatAfter: durationString(w.RepeatAfter),
		Severity: w.Severity,
	}
}
