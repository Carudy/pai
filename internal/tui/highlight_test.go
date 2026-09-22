package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The invariant that matters: highlighting is purely cosmetic. What the user
// reads to approve a command must be exactly the command that will run, so
// stripping the styling has to give the input back byte for byte.
func TestHighlightCommandPreservesText(t *testing.T) {
	cmds := []string{
		"ls -l /tmp",
		"a && b || c; d | e",
		`grep 'a|b' file`,
		`echo "hello $USER"`,
		`sed -n '1,20p' "$HOME/f"`,
		"sudo -n systemctl status netbird 2>&1 | head -5",
		`echo '---'`,
		"VAR=$HOME/bin ./run --flag=value",
		"awk '{print $1}' file | sort -u > out.txt",
		"unclosed 'quote",
		`echo "escaped \" quote"`,
		"# just a comment",
		"cmd; # trailing comment",
		"echo $? $1 $$ ${PATH}",
		"",
	}

	for _, cmd := range cmds {
		if got := ansi.Strip(highlightCommand(cmd)); got != cmd {
			t.Errorf("highlighting altered the command:\n in: %q\nout: %q", cmd, got)
		}
	}
}

// Tokens that carry meaning get styled; a command of plain words is left alone.
func TestHighlightCommandStylesTokens(t *testing.T) {
	rich := highlightCommand("grep -n 'x' file && echo $HOME")
	if !strings.Contains(rich, "\x1b[") {
		t.Fatalf("expected styling, got none: %q", rich)
	}

	plain := highlightCommand("ls /tmp")
	if strings.Contains(plain, "\x1b[") {
		t.Errorf("plain words should not be styled: %q", plain)
	}
}

// Operators and redirections are the safety-relevant part, so each is styled.
func TestHighlightCommandCoversOperators(t *testing.T) {
	for _, op := range []string{"&&", "||", "|", ";", ">", ">>", "<", "&"} {
		cmd := "a " + op + " b"
		out := highlightCommand(cmd)
		if !strings.Contains(out, "\x1b[") {
			t.Errorf("operator %q was not styled: %q", op, out)
		}
		if got := ansi.Strip(out); got != cmd {
			t.Errorf("operator %q changed the text: %q", op, got)
		}
	}
}
