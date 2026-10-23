package tool

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lookPath(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not available: %v", name, err)
	}
	return path
}

func TestExecuteArgvNoShell(t *testing.T) {
	printf := lookPath(t, "printf")
	var stream bytes.Buffer
	result, err := ExecuteArgv(context.Background(), ArgvSpec{
		Argv: []string{printf, "%s\n", "a; b", "$(echo injected)", "  spaced  ", ""},
	}, &stream)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.TimedOut {
		t.Fatalf("result = %+v", result)
	}
	const want = "a; b\n$(echo injected)\n  spaced  \n\n"
	if result.Output != want {
		t.Fatalf("output = %q, want %q", result.Output, want)
	}
	if stream.String() != want {
		t.Fatalf("stream = %q, want %q", stream.String(), want)
	}
}

func TestExecuteArgvExitCode(t *testing.T) {
	falseCmd := lookPath(t, "false")
	result, err := ExecuteArgv(context.Background(), ArgvSpec{Argv: []string{falseCmd}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("exit code = %d, want non-zero", result.ExitCode)
	}
}

func TestExecuteArgvMissingBinary(t *testing.T) {
	_, err := ExecuteArgv(context.Background(), ArgvSpec{
		Argv: []string{"pai-test-binary-that-does-not-exist"},
	}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestExecuteArgvTimeout(t *testing.T) {
	sleep := lookPath(t, "sleep")
	start := time.Now()
	result, err := ExecuteArgv(context.Background(), ArgvSpec{
		Argv:    []string{sleep, "5"},
		Timeout: 50 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || result.ExitCode != -1 {
		t.Fatalf("result = %+v", result)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}

func TestExecuteArgvCancellation(t *testing.T) {
	sleep := lookPath(t, "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	result, err := ExecuteArgv(ctx, ArgvSpec{Argv: []string{sleep, "5"}}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if result.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1", result.ExitCode)
	}
}

func TestExecuteArgvDir(t *testing.T) {
	pwd := lookPath(t, "pwd")
	dir := t.TempDir()
	result, err := ExecuteArgv(context.Background(), ArgvSpec{
		Argv: []string{pwd},
		Dir:  dir,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(result.Output)
	want := dir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		want = resolved
		if resolvedGot, err := filepath.EvalSymlinks(got); err == nil {
			got = resolvedGot
		}
	}
	if got != want {
		t.Fatalf("pwd = %q, want %q", got, want)
	}
}

func TestExecuteArgvEnvOverride(t *testing.T) {
	sh := lookPath(t, "sh")
	t.Setenv("PAI_TEST_ENV", "old")
	result, err := ExecuteArgv(context.Background(), ArgvSpec{
		Argv: []string{sh, "-c", `printf '%s' "$PAI_TEST_ENV"`},
		Env:  map[string]string{"PAI_TEST_ENV": "new"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "new" {
		t.Fatalf("output = %q, want new", result.Output)
	}
}

func TestExecuteArgvEmpty(t *testing.T) {
	if _, err := ExecuteArgv(context.Background(), ArgvSpec{}, nil); err == nil {
		t.Fatal("expected an error")
	}
}

func TestMergedEnvOverride(t *testing.T) {
	t.Setenv("PAI_TEST_ENV", "old")
	env := mergedEnv(map[string]string{"PAI_TEST_ENV": "new"})
	found := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, "PAI_TEST_ENV=") {
			found++
			if entry != "PAI_TEST_ENV=new" {
				t.Fatalf("entry = %q", entry)
			}
		}
	}
	if found != 1 {
		t.Fatalf("found %d entries, want 1", found)
	}
}
