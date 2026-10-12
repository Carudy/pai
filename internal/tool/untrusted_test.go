package tool

import "testing"

func TestUntrustedSegmentsAndNames(t *testing.T) {
	cmd := "ls -la && sudo rm -rf /tmp/x && cat f"
	trusted := []string{"ls", "cat"}

	if got := UntrustedSegments(cmd, trusted); len(got) != 1 || got[0] != 1 {
		t.Errorf("UntrustedSegments = %v, want [1]", got)
	}
	if names := UntrustedNames(cmd, trusted); len(names) != 1 || names[0] != "sudo" {
		t.Errorf("UntrustedNames = %v, want [sudo]", names)
	}

	// Nothing trusted: every segment is flagged.
	if n := len(UntrustedSegments(cmd, nil)); n != 3 {
		t.Errorf("empty trust list: %d untrusted, want 3", n)
	}
	// Everything trusted: none.
	if n := len(UntrustedSegments("ls && cat", trusted)); n != 0 {
		t.Errorf("fully trusted: %d untrusted, want 0", n)
	}
	// Names are deduplicated.
	if names := UntrustedNames("sudo a && sudo b", nil); len(names) != 1 {
		t.Errorf("names not deduplicated: %v", names)
	}
}

// A nameless segment (a construct) is flagged untrusted but offers no name to
// trust, since trusting a keyword would mean trusting everything it contains.
func TestUntrustedNamesSkipsConstructs(t *testing.T) {
	cmd := "ls && for i in 1; do echo $i; done"
	trusted := []string{"ls"}
	if got := UntrustedSegments(cmd, trusted); len(got) != 1 || got[0] != 1 {
		t.Errorf("UntrustedSegments = %v, want [1]", got)
	}
	if names := UntrustedNames(cmd, trusted); len(names) != 0 {
		t.Errorf("UntrustedNames = %v, want none (a construct has no name)", names)
	}
}
