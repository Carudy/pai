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
	// maxGroupGoal bounds the shared goal. It is rendered into every member's
	// prompt head, so a long paste would be paid for by the whole group every
	// wake.
	maxGroupGoal = 1 << 10
	// MaxMemberIntro bounds one member's in-group description. Unlike the goal it is
	// a line, not a paragraph: the roster block is shared by every member on every
	// wake, so a per-member budget has to stay small.
	MaxMemberIntro = 200
)

// NormalizeMemberIntro trims and flattens an in-group description onto one line.
// The text is rendered into the prompt head, where a newline would let one
// member's entry imitate the block's own structure, so whitespace is collapsed
// rather than preserved. An empty result means "no description".
func NormalizeMemberIntro(text string) (string, error) {
	flat := strings.Join(strings.Fields(text), " ")
	if len(flat) > MaxMemberIntro {
		return "", fmt.Errorf("a member description is longer than %d bytes", MaxMemberIntro)
	}
	return flat, nil
}

// GroupManifest is one parsed, normalized group definition.
type GroupManifest struct {
	Name       string
	Goal       string // optional shared goal, rendered into each member's head
	Lead       string // optional escalation hint; no routing role
	MaxMembers int
	Members    []string
	// MemberIntros is the in-group description of a member, keyed by name. It is
	// optional per member, so a manifest that does not use it reads exactly as
	// before.
	MemberIntros map[string]string
}

type groupFile struct {
	Name   string          `toml:"name"`
	Group  groupSection    `toml:"group"`
	Member []memberSection `toml:"member"`
}

type groupSection struct {
	Goal       string `toml:"goal"`
	Lead       string `toml:"lead"`
	MaxMembers int    `toml:"max_members"`
}

type memberSection struct {
	Name  string `toml:"name"`
	Intro string `toml:"intro"`
}

// ParseGroupManifest parses and validates a group manifest. Members must already
// exist as service sessions; that check belongs to the caller, which can see the
// session store.
func ParseGroupManifest(data []byte) (*GroupManifest, error) {
	var raw groupFile
	meta, err := toml.Decode(string(data), &raw)
	if err != nil {
		return nil, fmt.Errorf("parse group manifest: %w", err)
	}
	// A member is a reference to an existing session, so a key like `role` here
	// would read as if the group configured that session. It does not: only the
	// in-group description belongs to the membership.
	if keys := meta.Undecoded(); len(keys) > 0 {
		return nil, fmt.Errorf("unknown key %q in group manifest (a member names an existing session and, optionally, its in-group intro)", keys[0].String())
	}
	m := &GroupManifest{
		Name:       strings.TrimSpace(raw.Name),
		Goal:       strings.TrimSpace(raw.Group.Goal),
		Lead:       strings.TrimSpace(raw.Group.Lead),
		MaxMembers: raw.Group.MaxMembers,
	}
	if m.Name == "" {
		return nil, fmt.Errorf("a group manifest needs a name")
	}
	if len(m.Goal) > maxGroupGoal {
		return nil, fmt.Errorf("goal is longer than %d bytes", maxGroupGoal)
	}
	if m.MaxMembers == 0 {
		m.MaxMembers = DefaultGroupMaxMembers
	}
	if m.MaxMembers < 1 || m.MaxMembers > MaxGroupMembers {
		return nil, fmt.Errorf("max_members must be between 1 and %d", MaxGroupMembers)
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
		if strings.TrimSpace(entry.Intro) == "" {
			continue
		}
		intro, err := NormalizeMemberIntro(entry.Intro)
		if err != nil {
			return nil, fmt.Errorf("member %q: %w", name, err)
		}
		if m.MemberIntros == nil {
			m.MemberIntros = make(map[string]string, len(raw.Member))
		}
		m.MemberIntros[name] = intro
	}
	if len(m.Members) == 0 {
		return nil, fmt.Errorf("a group needs at least one [[member]]")
	}
	if len(m.Members) > m.MaxMembers {
		return nil, fmt.Errorf("%d members exceed max_members %d", len(m.Members), m.MaxMembers)
	}
	if m.Lead == "" {
		// No default: silently choosing a member would imply an authority the
		// group does not grant. A lead is only an escalation hint.
	} else if !seen[m.Lead] {
		return nil, fmt.Errorf("the lead %q must be one of the members", m.Lead)
	}
	return m, nil
}

// RenderGroupManifestTOML renders the manifest an operator would edit. It is the
// inverse of ParseGroupManifest, so a CLI editor can prefill the same shape.
func RenderGroupManifestTOML(m *GroupManifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "name = %q\n\n[group]\n", m.Name)
	if m.Goal != "" {
		fmt.Fprintf(&b, "goal = %q\n", m.Goal)
	}
	if m.Lead != "" {
		fmt.Fprintf(&b, "lead = %q\n", m.Lead)
	}
	fmt.Fprintf(&b, "max_members = %d\n", m.MaxMembers)
	for _, name := range m.Members {
		fmt.Fprintf(&b, "\n[[member]]\nname = %q\n", name)
	}
	return b.String()
}
