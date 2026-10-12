package hostinfo

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestDescribe(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/fish")
	t.Setenv("USER", "tester")

	got := Describe("/work/dir")
	for _, want := range []string{
		"OS: ",
		"(" + runtime.GOOS + " " + runtime.GOARCH + ")",
		"Shell: fish",
		"User: tester",
		"Datetime: ",
		"Working Dir: /work/dir\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe missing %q in:\n%s", want, got)
		}
	}
}

// An unset environment must degrade to placeholders, not empty fields.
func TestDescribeWithoutEnvironment(t *testing.T) {
	t.Setenv("SHELL", "")
	t.Setenv("USER", "")

	if got := Describe("/w"); !strings.Contains(got, "Shell: unknown User: unknown") {
		t.Errorf("missing placeholders:\n%s", got)
	}
}

// An empty workspace reports the process cwd without changing it.
func TestDescribeEmptyWorkingDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got := Describe(""); !strings.Contains(got, "Working Dir: "+wd+"\n") {
		t.Errorf("Describe(\"\") did not report the cwd:\n%s", got)
	}
	if after, _ := os.Getwd(); after != wd {
		t.Errorf("cwd changed: %s", after)
	}
}

func TestOSDetailIsNeverEmpty(t *testing.T) {
	if osDetail() == "" {
		t.Fatal("osDetail returned an empty description")
	}
}
