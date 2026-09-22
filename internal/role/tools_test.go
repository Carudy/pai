package role

import "testing"

func TestConfirmTitle(t *testing.T) {
	cases := []struct {
		name string
		verb string
		cmd  string
		want string
	}{
		{"single command is unchanged", "Execute this command?", "df -h", "Execute this command?"},
		{"chains report their command count", "Execute this command?", "a && b", "Execute this command? (2 commands)"},
		{"pipes count too", "Run on nb?", "a | b | c", "Run on nb? (3 commands)"},
		{"a quoted operator is not a separator", "Execute this command?", `grep 'a|b' f`, "Execute this command?"},
		{"backgrounding counts", "Execute this command?", "a & b", "Execute this command? (2 commands)"},
		{"empty command", "Execute this command?", "", "Execute this command?"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := confirmTitle(tc.verb, tc.cmd); got != tc.want {
				t.Errorf("confirmTitle(%q, %q) = %q, want %q", tc.verb, tc.cmd, got, tc.want)
			}
		})
	}
}
