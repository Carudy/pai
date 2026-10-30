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
	if m.MaxMembers != DefaultGroupMaxMembers || m.DailyWakes != DefaultGroupDailyWakes {
		t.Fatalf("defaults = %d/%d", m.MaxMembers, m.DailyWakes)
	}
	if len(m.Members) != 2 || m.Members[0] != "coordinator" || m.Members[1] != "hydrology" {
		t.Fatalf("members = %v", m.Members)
	}
}

// The lead is the address for `group:<name>`, so a manifest that names one
// outside the membership is a mistake worth catching before it is stored.
func TestParseGroupManifestRejectsBadLeads(t *testing.T) {
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

	// A manifest without a lead defaults to the first member.
	m, err := ParseGroupManifest([]byte("name = \"g\"\n[[member]]\nname = \"a\"\n"))
	if err != nil || m.Lead != "a" {
		t.Fatalf("default lead = %q, %v", m.Lead, err)
	}
}

func TestParseGroupManifestLimits(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"name = \"g\"\n[group]\nmax_members = 99\n[[member]]\nname = \"a\"\n", "max_members"},
		{"name = \"g\"\n[group]\ndaily_wakes = -1\n[[member]]\nname = \"a\"\n", "negative"},
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

// An explicit zero means unlimited, which is different from omitting the key.
func TestParseGroupManifestUnlimitedBudget(t *testing.T) {
	m, err := ParseGroupManifest([]byte("name = \"g\"\n[group]\ndaily_wakes = 0\n[[member]]\nname = \"a\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.DailyWakes != 0 {
		t.Fatalf("daily_wakes = %d, want 0", m.DailyWakes)
	}
}
