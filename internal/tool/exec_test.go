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

func names(segs []Segment) []string {
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = s.Name
	}
	return out
}

func ops(segs []Segment) []string {
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = s.Op
	}
	return out
}

func same(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }

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

		// Redirections must not be mistaken for separators. The printer normalises
		// the spacing around the redirect, so `&> log` comes back as `&>log`.
		{"fd redirect is not backgrounding", "cmd 2>&1 | head", []string{"cmd 2>&1", "head"}},
		{"redirect both", "cmd &> log", []string{"cmd &>log"}},
		{"redirect both (other form)", "cmd >& log", []string{"cmd >&log"}},

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

func TestSplitSegmentsOperators(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{"a && b | c", []string{"&&", "|", ""}},
		{"a || b; c", []string{"||", ";", ""}},
		{"a\nb", []string{"\n", ""}},
		{"a & b", []string{"&", ""}},
		{"a |& b", []string{"|&", ""}},
		{"a;", []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			if got := ops(SplitSegments(tc.cmd)); !same(got, tc.want) {
				t.Errorf("ops(%q) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// A heredoc body is data belonging to its command, not a run of commands: it must
// not produce segments named after the body or the terminator.
func TestSplitSegmentsHeredocBodyIsData(t *testing.T) {
	cases := []struct {
		cmd  string
		want int
	}{
		{"cat > notes.md <<'EOF'\nline one\nEOF", 1},
		{"cat <<-EOF\n\tindented\n\tEOF", 1},
		{"python3 - <<'PY'\nimport os\nPY", 1},
		// A pipeline of two commands: the heredoc body belongs to the first and
		// must not add segments of its own.
		{"cat <<EOF | grep x\nbody\nEOF", 2},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			segs := SplitSegments(tc.cmd)
			if len(segs) != tc.want {
				t.Fatalf("SplitSegments(%q) = %d segments (%v), want %d", tc.cmd, len(segs), texts(segs), tc.want)
			}
			for _, seg := range segs {
				if seg.Name == "body" || seg.Name == "EOF" || seg.Name == "line" || seg.Name == "PY" {
					t.Errorf("heredoc body leaked a segment named %q", seg.Name)
				}
			}
			// A heredoc feeds the command data, so the name alone is not the whole
			// story and the segment must not be trustable by name.
			if segs[0].Name != "" {
				t.Errorf("heredoc segment has name %q, want none", segs[0].Name)
			}
		})
	}
}

// A compound construct is one unit with no command name; it can never be waved
// through by trusting a keyword.
func TestSplitSegmentsCompoundIsOneUnnamedSegment(t *testing.T) {
	cmds := []string{
		"for i in 1 2 3; do echo $i; done",
		"while true; do echo x; break; done",
		"if true; then echo y; fi",
		"case $x in a) ls;; esac",
		"{ echo hi; ls; }",
		"(cd /tmp; ls)",
		"f() { ls; }",
		"> out.txt",
	}
	for _, cmd := range cmds {
		t.Run(cmd, func(t *testing.T) {
			segs := SplitSegments(cmd)
			if len(segs) != 1 {
				t.Fatalf("SplitSegments(%q) = %d segments (%v), want 1", cmd, len(segs), texts(segs))
			}
			if segs[0].Name != "" {
				t.Errorf("compound segment has name %q, want none", segs[0].Name)
			}
			if IsTrusted(cmd, []string{"for", "while", "if", "case", "echo", "ls", "cd", "f"}) {
				t.Errorf("IsTrusted(%q) = true; a construct must not be trusted by name", cmd)
			}
		})
	}
}

// Running the named command must be the whole story: a file write or a
// substitution hides code behind the command name.
func TestSplitSegmentsWithholdsNameWhenCommandIsNotTheWholeStory(t *testing.T) {
	hidden := []string{
		"echo x > f",
		"echo x >> f",
		"sort < in > out",
		"cmd &> log",
		"cmd >| clobber",
		"echo $(date)",
		"echo `date`",
		"diff <(a) <(b)",
	}
	for _, cmd := range hidden {
		t.Run(cmd, func(t *testing.T) {
			segs := SplitSegments(cmd)
			for _, seg := range segs {
				if seg.Name != "" {
					t.Errorf("SplitSegments(%q) named a segment %q; want no name", cmd, seg.Name)
				}
			}
		})
	}

	// A file-descriptor duplication is inert and keeps its name.
	segs := SplitSegments("ls 2>&1")
	if len(segs) != 1 || segs[0].Name != "ls" {
		t.Errorf("ls 2>&1 = %+v, want one segment named ls", segs)
	}
}

// The command name comes from argv[0], so a negation or a leading assignment does
// not hide it.
func TestSplitSegmentsNames(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{"ls -l /tmp", []string{"ls"}},
		{"/bin/ls -l", []string{"/bin/ls"}},
		{"'ls' -l", []string{"ls"}},
		{"! grep x f", []string{"grep"}},
		{"FOO=bar cmd arg", []string{"cmd"}},
		{"a && b | c", []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			if got := names(SplitSegments(tc.cmd)); !same(got, tc.want) {
				t.Errorf("names(%q) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// Text the parser rejects must fail closed: one unnamed segment that needs
// approval, never a name that could be trusted by accident.
func TestSplitSegmentsUnparseableFailsClosed(t *testing.T) {
	cmd := `echo "unterminated`
	segs := SplitSegments(cmd)
	if len(segs) != 1 {
		t.Fatalf("got %d segments, want 1", len(segs))
	}
	if segs[0].Name != "" {
		t.Errorf("unparseable text has name %q, want none", segs[0].Name)
	}
	if IsTrusted(cmd, []string{"echo"}) {
		t.Error("unparseable text was trusted")
	}
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

// Navigation builtins only change directory, so they need no grant and are never
// offered for trust. The hardening still holds: a write or substitution on the
// same chain has no name, so it still asks.
func TestNavigationBuiltinsAreExempt(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		list []string
		want bool
	}{
		{"cd alone needs no trust", "cd /tmp", nil, true},
		{"cd then a trusted command", "cd /tmp && ls", []string{"ls"}, true},
		{"pushd then a trusted command", "pushd /tmp && ls", []string{"ls"}, true},
		{"popd then a trusted command", "popd && pwd", []string{"pwd"}, true},
		{"cd does not vouch for an untrusted command", "cd /tmp && rm -rf /", []string{"cd"}, false},
		// The hardening: the write has no name, so the chain still asks.
		{"file write on the same chain still asks", "cd /x && echo y > f", []string{"cd", "echo"}, false},
		{"nameless cd form still asks", "cd /x > f", []string{"cd"}, false},
		{"substitution still asks", "cd $(x)", []string{"cd"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTrusted(tc.cmd, tc.list); got != tc.want {
				t.Errorf("IsTrusted(%q, %q) = %v, want %v", tc.cmd, tc.list, got, tc.want)
			}
		})
	}

	// A nav builtin is neither offered for trust nor flagged as needing approval.
	if got := UntrustedNames("cd /tmp && ls", nil); !same(got, []string{"ls"}) {
		t.Errorf("UntrustedNames = %q, want [ls] (cd must not be offered)", got)
	}
	if got := UntrustedNames("cd /tmp && ls", []string{"ls"}); len(got) != 0 {
		t.Errorf("UntrustedNames = %q, want empty", got)
	}
	if got := UntrustedSegments("cd /tmp && ls", nil); len(got) != 1 || got[0] != 1 {
		t.Errorf("UntrustedSegments = %v, want [1] (only ls)", got)
	}
}
