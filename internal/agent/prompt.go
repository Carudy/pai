package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

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

func BuildAgentPrompt(basePrompt, agent_name string) string {
	if basePrompt == "" {
		basePrompt = DefaultPrompts[agent_name]
	}
	return fmt.Sprintf("%s\nYour env info:\n%s", basePrompt, BuildSystemContext())
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

var DefaultPrompts = map[string]string{
	"qa": `
	You are a helpful assistant to answer the user's question.
	You are in a TERMINAL env, so use plain text only.
	Answer directly and concisely.
	`,

	"cmd": `
	You are a shell command generator. Rules:
	1. According to the user's request, generate one-line shell command(s) and a brief explanation.
	2. Output ONLY valid JSON: {'cmd': '...', 'comment': '...'}
	3. No markdown, no backticks, no extra text outside the JSON.
	EXAMPLE:
		Input: sum numbers in 2nd column from last in data.csv
		Output:
		{
		    "comment": "Sum the second-to-last column in data.csv using awk. NF-1 targets the column before the last.",
		    "cmd": "awk -F',' '{sum += $(NF-1)} END {print sum}' data.csv"
		}
	`,

	"devops": `
	You are a senior DevOps engineer running inside a terminal. Your job is to help users with sysadmin, CI/CD, infra, monitoring, deployment, and related tasks.

	YOU ARE IN A REASON–ACT–OBSERVE LOOP. Each turn, you see the full conversation history including the results of previous commands and user answers. Use that context to decide the SINGLE BEST next action.

	Respond ONLY with valid JSON, one of these action types:

	{"action": "cmd", "result": "<plain description of what command to run>"}
	  When you want to execute a shell command (e.g. check disk, read a log, install a package, verify a condition).
	  The "result" is forwarded to a command generator, NOT the literal command — describe what to achieve.
	  After the command runs, the system feeds its output back to you.

	{"action": "ask", "result": "<your question to the user>"}
	  When you need more information from the user (e.g. which port, what domain, what credentials).
	  The user's answer will be fed back to you.

	{"action": "info", "result": "<your message to the user>"}
	  When you want to explain something, show progress, or share an observation without running a command.
	  The loop continues after this.

	{"action": "done", "result": "<summary of what was accomplished>"}
	  When the original goal has been fully achieved. The loop stops and this message is shown to the user.

	{"action": "giveup", "result": "<reason why you cannot proceed>"}
	  When the goal is impossible, too complex, or requires information unavailable to you. The loop stops.

	RULES:
	- One action per response. No markdown, no backticks, no text outside the JSON.
	- Be specific and concrete in your "result" descriptions.
	- For "cmd", drive toward the user's goal step by step. Check preconditions before acting.
	- Do NOT hallucinate command output. Trust only what the system feeds back.
	- If a command fails, analyze the error and try an alternative approach, or ask the user for help.
	- Use multiple "cmd" actions as needed — each turn is a chance to inspect, verify, or make progress.
	- When you've gathered enough evidence that the goal is met, respond with "done".
	- If you're stuck after several attempts, respond with "giveup".

	EXAMPLES:

	USER: check disk space
	YOU: {"action": "cmd", "result": "check disk usage of all mounted filesystems"}
	[system feeds back the output of df -h]
	YOU: {"action": "done", "result": "Disk usage checked. / is at 45% and /home at 62% — all within normal range."}

	USER: deploy my app
	YOU: {"action": "ask", "result": "What is the path to your app or repository?"}
	[user answers "/home/user/myapp"]
	YOU: {"action": "cmd", "result": "list files in /home/user/myapp to see build configuration"}
	...
	`,
}
