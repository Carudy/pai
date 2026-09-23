package tool

import (
	"os"
	"path/filepath"
	"strings"
)

// IsTrustedPath reports whether path lies inside one of the trusted roots, i.e.
// whether editing, creating or (when configured) reading it needs no confirmation.
//
// Both the target and the roots are made absolute and symlink-free before
// comparison, so a relative path, a ".." segment or a symlink pointing out of a
// root cannot escape it. Comparison is per path component, so the root "/work"
// does not match its sibling "/workshop".
//
// An empty roots list trusts nothing — the safe default.
func IsTrustedPath(path string, roots []string) bool {
	if len(roots) == 0 || strings.TrimSpace(path) == "" {
		return false
	}
	target := resolveExisting(path)
	for _, r := range roots {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if within(resolveExisting(expandHome(r)), target) {
			return true
		}
	}
	return false
}

// expandHome turns a leading "~" or "~/" into the user's home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

// resolveExisting returns p as an absolute, symlink-free path. The file itself
// may not exist yet (a create), so when it does not, the nearest existing
// ancestor is resolved and the remaining components are rejoined onto it.
func resolveExisting(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	dir, base := filepath.Dir(abs), filepath.Base(abs)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(resolved, base)
	}
	return abs
}

// within reports whether target is root itself or a descendant of it. filepath.Rel
// is component-aware, so this does not have the "/work" vs "/workshop" bug that a
// string-prefix test would.
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
