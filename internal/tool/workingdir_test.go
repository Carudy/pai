package tool

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestExecuteCommandAtWorkspaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell command")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"one", "two"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "input"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		t.Run(content, func(t *testing.T) {
			t.Parallel()
			result, err := ExecuteCommandAt(context.Background(), "cat input > output; cat output", dir, nil)
			if err != nil || result.ExitCode != 0 || result.Output != content {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "output"))
			if err != nil || string(data) != content {
				t.Fatalf("file=%q err=%v", data, err)
			}
			if got, _ := os.Getwd(); got != cwd {
				t.Fatalf("cwd changed: %s", got)
			}
		})
	}
}

func TestIsTrustedPathAt(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if !IsTrustedPathAt("new", []string{"."}, dir) {
		t.Fatal("relative root should trust workspace")
	}
	if IsTrustedPathAt(filepath.Join(outside, "new"), []string{"."}, dir) {
		t.Fatal("trusted outside workspace")
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skip(err)
	}
	if IsTrustedPathAt("link/new", []string{"."}, dir) {
		t.Fatal("trusted symlink escape")
	}
}
