package tool

import (
	"strings"
	"testing"
)

func texts(segs []Segment) []string {
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = s.Text
	}
	return out
}

func TestSplitSegments(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want []string
	}{
		{"single command", "ls -l /tmp", []string{"ls -l /tmp"}},
		{"and", "a && b", []string{"a", "b"}},
		{"or", "a || b", []string{"a", "b"}},
		{"semicolon and pipe", "a; b | c", []string{"a", "b", "c"}},
		{"newline", "a\nb", []string{"a", "b"}},
		{"separators are reported", "a && b | c", []string{"a", "b", "c"}},
		{"empty", "", nil},
		{"trailing separator", "a;", []string{"a"}},

		// The point of the parser: an operator inside quotes is data.
		{"pipe inside single quotes", `grep 'a|b' f`, []string{`grep 'a|b' f`}},
		{"semicolon inside double quotes", `grep "a;b" f`, []string{`grep "a;b" f`}},
		{"and inside quotes", `echo 'x && y'`, []string{`echo 'x && y'`}},
		{"escaped quote does not end the string", `echo "a\"; b" x`, []string{`echo "a\"; b" x`}},
		{"escaped operator outside quotes", `echo a\; b`, []string{`echo a\; b`}},

		// Redirections must not be mistaken for separators.
		{"fd redirect is not backgrounding", "cmd 2>&1 | head", []string{"cmd 2>&1", "head"}},
		{"redirect both", "cmd &> log", []string{"cmd &> log"}},
		{"redirect both (other form)", "cmd >& log", []string{"cmd >& log"}},

		// A bare & does run what follows, so it separates.
		{"background", "a & b", []string{"a", "b"}},

		{"quoted operator then real one", `echo '---'; ls`, []string{`echo '---'`, "ls"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := texts(SplitSegments(tc.cmd))
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Errorf("SplitSegments(%q) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// Src is what the UI prints, so it must be the command verbatim.
func TestSegmentSrcIsVerbatim(t *testing.T) {
	cmds := []string{
		"a && b",
		"ls -l  /tmp | head -5",
		`grep 'a|b' f; echo done`,
		"a\nb",
		"cmd 2>&1 | head",
	}
	for _, cmd := range cmds {
		var joined []string
		for _, seg := range SplitSegments(cmd) {
			joined = append(joined, seg.Src)
		}
		rebuilt := strings.Join(joined, " ")
		// Whitespace may be normalised, but every non-space byte of the source
		// must survive: no characters are invented or dropped.
		if stripSpaces(rebuilt) != stripSpaces(cmd) {
			t.Errorf("Src lost or changed content:\n in: %q\nout: %q", cmd, rebuilt)
		}
	}
}

func stripSpaces(s string) string {
	return strings.Join(strings.Fields(s), "")
}

func TestIsTrusted(t *testing.T) {
	cases := []struct {
		name    string
		cmd     string
		trusted []string
		want    bool
	}{
		{"nothing is trusted by default", "ls", nil, false},
		{"simple trusted command", "ls -l", []string{"ls"}, true},
		{"simple untrusted command", "rm -rf /", []string{"ls"}, false},
		{"chain of trusted commands", "ls && echo hi", []string{"ls", "echo"}, true},
		{"chain with one untrusted", "ls && rm -rf /", []string{"ls", "echo"}, false},

		// The fix: a quoted operator is data, not a second command.
		{"quoted pipe", `grep 'a|b' file`, []string{"grep"}, true},
		{"quoted semicolon", `grep "a;b" file`, []string{"grep"}, true},

		// The hole this closes: backgrounding still runs what follows.
		{"backgrounded second command", "ls & rm -rf /", []string{"ls"}, false},
		{"backgrounded both trusted", "ls & echo hi", []string{"ls", "echo"}, true},

		// Redirections are part of one command.
		{"fd redirect needs only its own command trusted", "ls 2>&1", []string{"ls"}, true},
		{"pipe after redirect still counts", "ls 2>&1 | head -5", []string{"ls"}, false},
		{"pipe after redirect, both trusted", "ls 2>&1 | head -5", []string{"ls", "head"}, true},

		// Full paths and bare names are interchangeable.
		{"path form", "/bin/ls -l", []string{"ls"}, true},
		{"path in trusted list", "ls -l", []string{"/bin/ls"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTrusted(tc.cmd, tc.trusted); got != tc.want {
				t.Errorf("IsTrusted(%q, %q) = %v, want %v", tc.cmd, tc.trusted, got, tc.want)
			}
		})
	}
}
