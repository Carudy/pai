package chat

import (
	"strings"
	"testing"
	"time"
)

// The group block is part of the frozen head: the roster uses exactly the names
// `mail.to` accepts, sorted so the bytes never drift between runs.
func TestSetGroupRendersTheBrief(t *testing.T) {
	rp, err := LoadRolePromptForSession("devops", "", nil, nil, t.TempDir(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	rp.SetGroup(GroupBrief{
		Name:    "hydro",
		Goal:    "keep the gates safe during maintenance",
		Lead:    "coord",
		Members: []string{"met", "coord", "hydro"},
	})
	head := rp.Head()
	for _, want := range []string{
		"## Group: hydro",
		"Shared goal: keep the gates safe during maintenance",
		"Members: coord (lead), hydro, met.",
		"`group:hydro` reaches every member",
		"## Tool: mail",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("head is missing %q:\n%s", want, head)
		}
	}
	// The caller's slice is untouched and the block is stable across renders.
	if rp.group.Members[0] != "coord" {
		t.Fatalf("members were not copied: %v", rp.group.Members)
	}
	if again := rp.Head(); again != head {
		t.Fatal("the head is not deterministic")
	}
}

// A group without a shared goal or lead paints only what exists; nothing is
// invented on the model's behalf.
func TestGroupBlockOmitsAbsentParts(t *testing.T) {
	rp, err := LoadRolePromptForSession("devops", "", nil, nil, t.TempDir(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	rp.SetGroup(GroupBrief{Name: "hydro", Members: []string{"a", "b"}})
	head := rp.Head()
	if strings.Contains(head, "Shared goal:") || strings.Contains(head, "(lead)") {
		t.Fatalf("absent parts were rendered:\n%s", head)
	}
	if !strings.Contains(head, "Members: a, b.") {
		t.Fatalf("roster missing:\n%s", head)
	}
}

// The addressing sentence is only true when the session can actually send mail.
func TestGroupBlockNeedsMail(t *testing.T) {
	rp, err := LoadRolePromptForSession("coder", "", nil, nil, t.TempDir(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if rp.hasTool("mail") {
		t.Fatal("the coder role unexpectedly resolves mail")
	}
	rp.SetGroup(GroupBrief{Name: "hydro", Members: []string{"a"}})
	if strings.Contains(rp.Head(), "Mail: address") {
		t.Fatalf("a session without mail was told how to send it:\n%s", rp.Head())
	}
}

// An ungrouped session must keep byte-identical prompt bytes: the block is
// absent, not empty.
func TestUngroupedHeadIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	plain, err := LoadRolePromptForSession("coder", "", nil, nil, dir, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := LoadRolePromptForSession("coder", "", nil, nil, dir, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	cleared.SetGroup(GroupBrief{})
	if plain.Head() != cleared.Head() {
		t.Fatalf("clearing a group changed the head:\n--- plain ---\n%s\n--- cleared ---\n%s", plain.Head(), cleared.Head())
	}
	if strings.Contains(plain.Head(), "## Group:") {
		t.Fatal("an ungrouped session rendered a group block")
	}
}
