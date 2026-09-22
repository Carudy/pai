package tui

import "strings"

// highlightCommand colours the parts of a shell command that are easy to miss in
// a long line — quoted strings, operators, flags and variables — so a chained
// command can be scanned before it is approved.
//
// It only inserts styling: the visible text stays byte-for-byte the command, so
// what is read is exactly what will run. Plain words are left unstyled.
func highlightCommand(cmd string) string {
	var b strings.Builder
	for i := 0; i < len(cmd); {
		switch c := cmd[i]; {
		case c == ' ' || c == '\t':
			b.WriteByte(c)
			i++

		case c == '\'' || c == '"':
			j := endOfQuote(cmd, i)
			b.WriteString(RenderStr("CmdString", cmd[i:j]))
			i = j

		case c == '#' && (i == 0 || cmd[i-1] == ' ' || cmd[i-1] == '\t'):
			// A comment runs to the end of the line.
			b.WriteString(RenderStr("CmdComment", cmd[i:]))
			i = len(cmd)

		case c == '$':
			j := endOfVariable(cmd, i)
			b.WriteString(RenderStr("CmdVar", cmd[i:j]))
			i = j

		case strings.IndexByte("|&;<>", c) >= 0:
			j := i
			for j < len(cmd) && strings.IndexByte("|&;<>", cmd[j]) >= 0 {
				j++
			}
			b.WriteString(RenderStr("CmdOperator", cmd[i:j]))
			i = j

		default:
			j := endOfWord(cmd, i)
			if w := cmd[i:j]; strings.HasPrefix(w, "-") && w != "-" {
				b.WriteString(RenderStr("CmdFlag", w))
			} else {
				b.WriteString(w)
			}
			i = j
		}
	}
	return b.String()
}

// endOfQuote returns the index just past the quoted run starting at i, or len(s)
// when the quote is never closed. Backslash escapes are honoured inside double
// quotes; inside single quotes everything is literal, as in the shell.
func endOfQuote(s string, i int) int {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		if s[j] == '\\' && q == '"' {
			j++
			continue
		}
		if s[j] == q {
			return j + 1
		}
	}
	return len(s)
}

// endOfVariable returns the index just past a $name, ${name} or $special run.
func endOfVariable(s string, i int) int {
	j := i + 1
	if j >= len(s) {
		return j
	}
	if s[j] == '{' {
		for k := j + 1; k < len(s); k++ {
			if s[k] == '}' {
				return k + 1
			}
		}
		return len(s)
	}
	for j < len(s) && isNameByte(s[j]) {
		j++
	}
	if j == i+1 {
		// A special parameter: $?, $!, $#, $$ and friends take one character.
		j++
	}
	return j
}

func isNameByte(c byte) bool {
	return c == '_' ||
		c >= 'a' && c <= 'z' ||
		c >= 'A' && c <= 'Z' ||
		c >= '0' && c <= '9'
}

// endOfWord returns the index just past the next plain word. It always advances:
// every character that could stop it is handled before the caller reaches here.
func endOfWord(s string, i int) int {
	for j := i; j < len(s); j++ {
		c := s[j]
		if c == ' ' || c == '\t' || c == '\'' || c == '"' || c == '$' ||
			strings.IndexByte("|&;<>", c) >= 0 {
			return j
		}
	}
	return len(s)
}
