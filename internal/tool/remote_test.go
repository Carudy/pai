package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteCmd(t *testing.T) {
	plain := &RemoteManager{}
	if got := plain.remoteCmd("df -h"); got != "df -h" {
		t.Errorf("no shell: got %q, want the command unchanged", got)
	}

	rm := &RemoteManager{shell: "bash"}
	if got, want := rm.remoteCmd("netbird status"), `bash -lc 'netbird status'`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// A value with a space is a verbatim prefix (no implicit -lc).
	full := &RemoteManager{shell: "bash -lc"}
	if got, want := full.remoteCmd("netbird status"), `bash -lc 'netbird status'`; got != want {
		t.Errorf("explicit prefix: got %q, want %q", got, want)
	}
}

// The quoted command must survive the remote login shell's own parse — that's
// the whole point of shellQuote, and where naive quoting breaks.
func TestShellQuoteRoundTrip(t *testing.T) {
	cases := []string{
		"ls /usr/local/bin",
		`grep 'a|b' file`,
		`echo "it's $HOME"`,
		"echo ok && touch /tmp/x",
	}
	for _, c := range cases {
		rm := &RemoteManager{shell: "bash"}
		// Emulate the remote shell re-parsing: bash -lc is handed the quoted
		// word, so the inner text must be exactly the original.
		wrapped := rm.remoteCmd(c)
		quoted := wrapped[len("bash -lc "):]
		if len(quoted) < 2 || quoted[0] != '\'' || quoted[len(quoted)-1] != '\'' {
			t.Fatalf("not single-quoted: %q", wrapped)
		}
		inner := quoted[1 : len(quoted)-1]
		if unescaped := unquoteSingle(inner); unescaped != c {
			t.Errorf("round trip changed command:\n got %q\nwant %q", unescaped, c)
		}
	}
}

// unquoteSingle reverses shellQuote's escaping of embedded single quotes.
func unquoteSingle(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' && i+3 < len(s) && s[i+1] == '\\' && s[i+2] == '\'' && s[i+3] == '\'' {
			out = append(out, '\'')
			i += 3
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

// TestExecuteRemoteWrapsShell proves the wrapper survives ssh's own argument
// handling: a fake ssh records its argv, and the command must arrive as a single
// argument (ssh joins argv with spaces before the remote shell re-parses it).
func TestExecuteRemoteWrapsShell(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record + "\n"
	sshPath := filepath.Join(dir, "ssh")
	if err := os.WriteFile(sshPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	rm := &RemoteManager{controlDir: dir, shell: "bash"}
	_, err := rm.ExecuteRemote(context.Background(), RemotePayload{Host: "h", Cmd: "a && b"}, nil)
	if err != nil {
		t.Fatalf("ExecuteRemote: %v", err)
	}

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(args) == 0 {
		t.Fatal("no argv recorded")
	}
	got := args[len(args)-1]
	if want := `bash -lc 'a && b'`; got != want {
		t.Errorf("remote command arg = %q, want %q", got, want)
	}
}
