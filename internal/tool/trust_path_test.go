package tool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsTrustedPath(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	sibling := root + "-sibling"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sibling) })

	cases := []struct {
		name  string
		path  string
		roots []string
		want  bool
	}{
		{"empty roots trust nothing", filepath.Join(root, "a.txt"), nil, false},
		{"exact root", root, []string{root}, true},
		{"descendant", filepath.Join(sub, "a.txt"), []string{root}, true},
		{"new file under root", filepath.Join(sub, "not-yet.txt"), []string{root}, true},
		{"a user parent escape", filepath.Join(root, "..", "outside.txt"), []string{root}, false},
		{"a sibling with the root as a prefix", filepath.Join(sibling, "a.txt"), []string{root}, false},
		{"an unrelated path", t.TempDir(), []string{root}, false},
		{"second root matches", filepath.Join(sub, "a.txt"), []string{"/nonexistent", root}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTrustedPath(tc.path, tc.roots); got != tc.want {
				t.Errorf("IsTrustedPath(%q, %v) = %v, want %v", tc.path, tc.roots, got, tc.want)
			}
		})
	}
}

// A symlink inside a trusted root that points outside it must not widen the
// trust: the resolved location, not the link's, is what counts.
func TestIsTrustedPathResolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if IsTrustedPath(filepath.Join(link, "secret.txt"), []string{root}) {
		t.Error("a symlink escaped the trusted root")
	}
}

func TestIsTrustedPathExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if !IsTrustedPath(filepath.Join(home, "work", "a.txt"), []string{"~/work"}) {
		t.Error("~/work did not expand to the home directory")
	}
	if IsTrustedPath(filepath.Join(home, "other", "a.txt"), []string{"~/work"}) {
		t.Error("a path outside ~/work was trusted")
	}
}
