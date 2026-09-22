package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SetScalar sets `<key> = <value>` inside `[section]` of the TOML file at path,
// preserving the rest of the file (comments included) by editing only the
// affected line, inserting the key if absent, or appending the section if it
// does not exist.
//
// value must already be TOML-formatted (e.g. `"x"`, `true`, `42`). Only
// single-line scalars are supported — this is deliberately not a general TOML
// writer, so arrays/inline tables are left to hand editing.
func SetScalar(path, section, key, value string) error {
	lines, err := readLines(path)
	if err != nil {
		return err
	}

	start, end := sectionBounds(lines, section)
	if start < 0 {
		lines = append(lines, "", "["+section+"]", key+" = "+value)
		return writeLines(path, lines)
	}

	keyRe := regexp.MustCompile(`^(\s*)` + regexp.QuoteMeta(key) + `\s*=`)
	for i := start + 1; i < end; i++ {
		if m := keyRe.FindStringSubmatch(lines[i]); m != nil {
			lines[i] = m[1] + key + " = " + value
			return writeLines(path, lines)
		}
	}

	// Insert directly after the section header.
	insert := append([]string{key + " = " + value}, lines[start+1:]...)
	lines = append(lines[:start+1], insert...)
	return writeLines(path, lines)
}

// UnsetScalar removes `<key>` from `[section]`. Removing a key that is not
// present is not an error.
func UnsetScalar(path, section, key string) error {
	lines, err := readLines(path)
	if err != nil {
		return err
	}

	start, end := sectionBounds(lines, section)
	if start < 0 {
		return nil
	}

	keyRe := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	kept := make([]string, 0, len(lines))
	for i, ln := range lines {
		if i > start && i < end && keyRe.MatchString(ln) {
			continue
		}
		kept = append(kept, ln)
	}
	return writeLines(path, kept)
}

// sectionBounds returns the line range (start, end) of `[section]`, where end is
// the index of the next section header (or len(lines)). start is -1 if absent.
func sectionBounds(lines []string, section string) (start, end int) {
	header := "[" + section + "]"
	start, end = -1, len(lines)
	for i, ln := range lines {
		if strings.TrimSpace(ln) == header {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(strings.TrimSpace(ln), "[") {
			end = i
			break
		}
	}
	return start, end
}

// readLines returns the file's lines, or an empty slice if it does not exist.
func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n"), nil
}

func writeLines(path string, lines []string) error {
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}
