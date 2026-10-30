package config

import (
	"strings"
	"testing"
)

func TestParseGroupManifestDefaults(t *testing.T) {
	m, err := ParseGroupManifest([]byte(`
name = "hydro"

[group]
lead = "coordinator"

[[member]]
name = "coordinator"

[[member]]
name = "hydrology"
`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "hydro" || m.Lead != "coordinator" {
		t.Fatalf("manifest = %+v", m)
	}
	if m.MaxMembers != DefaultGroupMaxMembers {
		t.Fatalf("max_members = %d", m.MaxMembers)
	}
	if len(m.Members) != 2 || m.Members[0] != "coordinator" || m.Members[1] != "hydrology" {
		t.Fatalf("members = %v", m.Members)
	}
}

// The lead is an escalation hint, not an address, so it is optional; when it is
// present it must be a member.
func TestParseGroupManifestLead(t *testing.T) {
	_, err := ParseGroupManifest([]byte(`
name = "g"
[group]
lead = "stranger"
[[member]]
name = "a"
`))
	if err == nil || !strings.Contains(err.Error(), "must be one of the members") {
		t.Fatalf("err = %v", err)
	}

	// Omitting the lead is valid, and no member is silently promoted.
	m, err := ParseGroupManifest([]byte("name = \"g\"\n[[member]]\nname = \"a\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Lead != "" {
		t.Fatalf("lead = %q, want none", m.Lead)
	}
}

func TestParseGroupManifestLimits(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"name = \"g\"\n[group]\nmax_members = 99\n[[member]]\nname = \"a\"\n", "max_members"},
		{"name = \"g\"\n", "at least one"},
		{"[group]\nlead = \"a\"\n", "needs a name"},
		{"name = \"g\"\n[[member]]\nname = \"a\"\n[[member]]\nname = \"a\"\n", "twice"},
		{"name = \"g\"\n[group]\nmax_members = 1\n[[member]]\nname = \"a\"\n[[member]]\nname = \"b\"\n", "exceed"},
	} {
		if _, err := ParseGroupManifest([]byte(tc.body)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want %q", tc.body, err, tc.want)
		}
	}
}

// An unknown key is a mistake worth catching: a group describes membership and a
// shared goal, not a member's role, and silently ignoring `role` would read as if
// it did.
func TestParseGroupManifestRejectsUnknownKeys(t *testing.T) {
	_, err := ParseGroupManifest([]byte("name = \"g\"\n[[member]]\nname = \"a\"\nrole = \"devops\"\n"))
	if err == nil || !strings.Contains(err.Error(), "role") {
		t.Fatalf("err = %v, want an unknown-key refusal", err)
	}
}
