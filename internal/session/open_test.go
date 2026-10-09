package session

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Carudy/pai/internal/paths"
)

func TestOpenExclusive(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	first, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	assertLocked(t)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open()
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	defer second.Close()
	if err := first.Close(); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
	assertLocked(t)
}

func assertLocked(t *testing.T) {
	t.Helper()
	store, err := Open()
	if store != nil {
		store.Close()
		t.Fatal("second Open unexpectedly succeeded")
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open error = %v, want ErrLocked", err)
	}
}

func TestOpenFailureReleasesOwnership(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := paths.DataDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var obstacle string
	if Backend() == "file" {
		obstacle = filepath.Join(dir, "sessions")
		if err := os.WriteFile(obstacle, nil, 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		obstacle = filepath.Join(dir, "sessions.db")
		if err := os.Mkdir(obstacle, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if store, err := Open(); err == nil {
		store.Close()
		t.Fatal("Open with blocked backend unexpectedly succeeded")
	} else if errors.Is(err, ErrLocked) {
		t.Fatalf("unexpected lock conflict: %v", err)
	}
	if err := os.Remove(obstacle); err != nil {
		t.Fatal(err)
	}
	store, err := Open()
	if err != nil {
		t.Fatalf("reopen after failed backend: %v", err)
	}
	defer store.Close()
}

func TestOpenProcessOwnership(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoreOwnerHelper$")
	cmd.Env = append(os.Environ(), "PAI_TEST_STORE_OWNER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("child did not acquire store ownership")
	}
	assertLocked(t)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	waited = true
	store, err := Open()
	if err != nil {
		t.Fatalf("reopen after owner crash: %v", err)
	}
	defer store.Close()
}

func TestStoreOwnerHelper(t *testing.T) {
	if os.Getenv("PAI_TEST_STORE_OWNER") != "1" {
		return
	}
	store, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fmt.Println("ready")
	// The parent kills us without Close to verify kernel-owned cleanup.
	bufio.NewReader(os.Stdin).ReadByte()
}
