package config

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// Group manifests describe a membership boundary over existing service
// sessions. They carry policy, not behaviour: a member keeps its own role,
// model, and workspace, so it can be moved between groups without its
// conversation changing.
const (
	// DefaultGroupMaxMembers matches the residency guidance: a group larger than
	// this costs more to keep prepared than the coordination it saves.
	DefaultGroupMaxMembers = 8
	// MaxGroupMembers is a structural cap. Fan-out and listing cost grow with the
	// member count, and a group is a working set, not an org chart.
	MaxGroupMembers = 32
	// DefaultGroupDailyWakes bounds non-user activations per member per day. A
	// human instruction is never counted or refused.
	DefaultGroupDailyWakes = 500
)

// GroupManifest is one parsed, normalized group definition.
type GroupManifest struct {
	Name       string
	Lead       string
	MaxMembers int
	DailyWakes int // 0 = unlimited
	Members    []string
}

type groupFile struct {
	Name   string          `toml:"name"`
	Group  groupSection    `toml:"group"`
	Member []memberSection `toml:"member"`
}

type groupSection struct {
	Lead       string `toml:"lead"`
	MaxMembers int    `toml:"max_members"`
	DailyWakes *int   `toml:"daily_wakes"`
}

type memberSection struct {
	Name string `toml:"name"`
}

// ParseGroupManifest parses and validates a group manifest. Members must already
// exist as service sessions; that check belongs to the caller, which can see the
// session store.
func ParseGroupManifest(data []byte) (*GroupManifest, error) {
	var raw groupFile
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, fmt.Errorf("parse group manifest: %w", err)
	}
	m := &GroupManifest{
		Name:       strings.TrimSpace(raw.Name),
		Lead:       strings.TrimSpace(raw.Group.Lead),
		MaxMembers: raw.Group.MaxMembers,
	}
	if m.Name == "" {
		return nil, fmt.Errorf("a group manifest needs a name")
	}
	if m.MaxMembers == 0 {
		m.MaxMembers = DefaultGroupMaxMembers
	}
	if m.MaxMembers < 1 || m.MaxMembers > MaxGroupMembers {
		return nil, fmt.Errorf("max_members must be between 1 and %d", MaxGroupMembers)
	}
	if raw.Group.DailyWakes == nil {
		m.DailyWakes = DefaultGroupDailyWakes
	} else {
		m.DailyWakes = *raw.Group.DailyWakes
		if m.DailyWakes < 0 {
			return nil, fmt.Errorf("daily_wakes must not be negative")
		}
	}
	seen := make(map[string]bool, len(raw.Member))
	for _, entry := range raw.Member {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			return nil, fmt.Errorf("a [[member]] entry needs a name")
		}
		if seen[name] {
			return nil, fmt.Errorf("member %q is listed twice", name)
		}
		seen[name] = true
		m.Members = append(m.Members, name)
	}
	if len(m.Members) == 0 {
		return nil, fmt.Errorf("a group needs at least one [[member]]")
	}
	if len(m.Members) > m.MaxMembers {
		return nil, fmt.Errorf("%d members exceed max_members %d", len(m.Members), m.MaxMembers)
	}
	if m.Lead == "" {
		// One member is the common case for a first group; defaulting the lead
		// keeps the manifest short without making an ambiguous one silently pick.
		m.Lead = m.Members[0]
	}
	if !seen[m.Lead] {
		return nil, fmt.Errorf("the lead %q must be one of the members", m.Lead)
	}
	return m, nil
}

// RenderGroupManifestTOML renders the manifest an operator would edit. It is the
// inverse of ParseGroupManifest, so a CLI editor can prefill the same shape.
func RenderGroupManifestTOML(m *GroupManifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "name = %q\n\n[group]\n", m.Name)
	fmt.Fprintf(&b, "lead        = %q\n", m.Lead)
	fmt.Fprintf(&b, "max_members = %d\n", m.MaxMembers)
	fmt.Fprintf(&b, "daily_wakes = %d\n", m.DailyWakes)
	for _, name := range m.Members {
		fmt.Fprintf(&b, "\n[[member]]\nname = %q\n", name)
	}
	return b.String()
}
