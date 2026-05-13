package agent

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Carudy/pai/internal/config"
)

const SelfAware = `
Your name is PAI (Personal Agent Inside Terminal);
You're an agent system built upon LLMs, you're developed with Golang;
`

//go:embed prompts/*.md
var embeddedPrompts embed.FS

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

func LoadAgentPrompt(name string, customContent config.CustomPrompt) (string, error) {
	var body string
	pure_prompt := strings.TrimSpace(customContent.Prompt)

	if (!customContent.Additional) && pure_prompt != "" {
		body = pure_prompt
	} else {
		// Try disk first (dev hot-reload), then fall back to embedded
		diskPath := filepath.Join("internal", "agent", "prompts", name+".md")
		if data, err := os.ReadFile(diskPath); err == nil {
			body = string(data)
		} else {
			data, err := embeddedPrompts.ReadFile("prompts/" + name + ".md")
			if err != nil {
				return "", fmt.Errorf("prompt %q not found on disk or embedded: %w", name, err)
			}
			body = string(data)
		}
		if customContent.Additional {
			body = fmt.Sprintf("%s\n[User:]\n%s", body, pure_prompt)
		}
	}

	return fmt.Sprintf("%s\n%s\nYour Terminal Info:\n%s", SelfAware, body, BuildSystemContext()), nil
}
