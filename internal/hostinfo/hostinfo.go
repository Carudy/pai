// Package hostinfo reports the host facts PAI states in the system prompt. It is
// a leaf package so the conversation protocol never shells out: probing the host
// is a host concern, not a protocol one.
package hostinfo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Describe renders the terminal/environment block: OS detail, platform, shell,
// user, workspace, and when this session began. An empty workingDir reports the
// process cwd without changing it.
//
// born is the session's creation time, or the zero time for an ephemeral run. It is
// deliberately not "now": the head must stay byte-identical between requests so the
// provider's prefix cache can match, and a clock that ticks would invalidate that
// cache on every single turn. The prompt states the session's start and no live
// clock; the model can run `date` when it needs the current time.
func Describe(workingDir string, born time.Time) string {
	shell := os.Getenv("SHELL")
	if shell != "" {
		shell = filepath.Base(shell)
	} else {
		shell = "unknown"
	}

	user := os.Getenv("USER")
	if user == "" {
		user = "unknown"
	}

	if workingDir == "" {
		workingDir, _ = os.Getwd()
	}

	block := fmt.Sprintf("OS: %s (%s %s)\nShell: %s User: %s\nWorking Dir: %s\n",
		osDetail(), runtime.GOOS, runtime.GOARCH, shell, user, workingDir)
	if !born.IsZero() {
		block += fmt.Sprintf("Session started: %s\n", born.Format(DateTimeFormat))
	}
	return block
}

// DateTimeFormat is the second-resolution stamp the prompt head uses for the session
// start.
const DateTimeFormat = "2006-01-02 15:04:05"

// Identity renders "user@host" for a frontend to show which machine is serving
// it. It is display metadata, so a missing value degrades to "unknown" rather
// than failing.
func Identity() string {
	user := os.Getenv("USER")
	if user == "" {
		user = "unknown"
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return user + "@" + host
}

// osDetail prefers a friendly distribution name, falling back to the platform.
func osDetail() string {
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile("/etc/os-release")
		if err == nil {
			lines := strings.SplitSeq(string(data), "\n")
			for line := range lines {
				if strings.HasPrefix(line, "PRETTY_NAME=") {
					return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
				}
			}
		}
		return "Linux"
	case "darwin":
		out, err := exec.Command("sw_vers", "-productVersion").Output()
		if err == nil {
			return fmt.Sprintf("macOS %s", strings.TrimSpace(string(out)))
		}
		return "macOS"
	case "windows":
		return "Windows"
	default:
		return runtime.GOOS
	}
}
