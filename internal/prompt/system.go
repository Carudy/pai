package prompt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const DefaultAskPrompt = `You are a helpful assistant. Answer the user's question directly.
Remember you are in a terminal environment. So don't response with markdown-like formatting.
Use plain text that is directly readable only.
And response concisely, and as short as possible.
`

const DefaultCommandPrompt = `You are a shell command generator. Rules:
1. According to the user's request, generate one-line shell command(s) and a brief explanation.
2. Output ONLY valid JSON: {\"cmd\": \"your_shell_command\", \"comment\": \"brief explanation\"}
3. No markdown, no backticks, no extra text.`

func BuildSystemContext() string {
	osDetail := getOSDetail()

	shell := os.Getenv("SHELL")
	if shell != "" {
		shell = filepath.Base(shell)
	} else {
		shell = "unknown"
	}

	userInfo := os.Getenv("USER")
	if userInfo == "" {
		userInfo = "unknown"
	}

	now := time.Now()
	dateTime := fmt.Sprintf("%s %s", now.Format("2006-01-02"), now.Format("15:04:05"))

	wd, _ := os.Getwd()

	return fmt.Sprintf("OS: %s (%s %s)\nShell: %s User: %s\nDatetime: %s\nWorking Dir: %s\n",
		osDetail, runtime.GOOS, runtime.GOARCH, shell, userInfo, dateTime, wd)
}

func BuildAskSystemPrompt(basePrompt string) string {
	return fmt.Sprintf("%s\n%s", basePrompt, BuildSystemContext())
}

func BuildCommandSystemPrompt(basePrompt string) string {
	return fmt.Sprintf("%s\nRemember your env info:\n%s", basePrompt, BuildSystemContext())
}

func getOSDetail() string {
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
		cmd := exec.Command("sw_vers", "-productVersion")
		out, err := cmd.Output()
		if err == nil {
			version := strings.TrimSpace(string(out))
			return fmt.Sprintf("macOS %s", version)
		}
		return "macOS"
	case "windows":
		return "Windows"
	default:
		return runtime.GOOS
	}
}
