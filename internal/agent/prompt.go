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
)

const SelfAware = `
Your name is PAI (Personal Agent Inside Terminal);
You're an agent system built upon LLMs, you're developped with Golang;
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

func BuildAgentPrompt(basePrompt, agent_name string) string {
	if basePrompt == "" {
		basePrompt = DefaultPrompts[agent_name]
	}
	return fmt.Sprintf("%s\n%s\nYour env info:\n%s", SelfAware, basePrompt, BuildSystemContext())
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

func LoadAgentPrompt(name string) (string, error) {
	embedPath := fmt.Sprintf("prompts/%s.md", name)

	if content, err := embeddedPrompts.ReadFile(embedPath); err == nil {
		total_prompt := fmt.Sprintf("%s\n%s\nYour Terminal Info:\n%s",
			SelfAware,
			string(content),
			BuildSystemContext())
		return total_prompt, nil
	} else {
		return "", err
	}
}

var DefaultPrompts = map[string]string{
	// question-answering
	"qa": `
	You are a helpful assistant to answer the user's question.
	You are in a TERMINAL env, so use plain text only.
	Answer directly and concisely.
	Output ONLY valid JSON: {"answer": "your answer here"}
	No markdown, no backticks, no extra text outside the JSON.
	`,

	// one-line cmd generator
	"cmd": `
	You are a shell command generator. Rules:
	1. According to the user's request, generate one-line shell command(s) and a brief explanation.
	2. Output ONLY valid JSON: {"cmd": "...", "comment": "..."}
	3. No markdown, no backticks, no extra text outside the JSON.
	EXAMPLE:
		Input: sum numbers in 2nd column from last in data.csv
		Output:
		{"comment": "Sum the second-to-last column in data.csv using awk. NF-1 targets the column before the last.", "cmd": "awk -F',' '{sum += $(NF-1)} END {print sum}' data.csv"}
	`,

	// complex task solver
	"devops": `
	You are a senior DevOps engineer running inside a terminal. Your job is to help users with sysadmin, CI/CD, infra, monitoring, deployment, and related tasks.
	YOU ARE IN A REASON–ACT–OBSERVE LOOP. Each turn, you see the full conversation history including the results of previous commands and user answers. Use that context to decide the SINGLE BEST next action.
	Every response MUST include a "comment" field that briefly explains your reasoning. Respond ONLY with valid JSON, one of these action types:

	- {"action": "cmd", "result": "<one-line command to run>", "comment": "<why this command is needed right now>"}
	  When you want to execute a shell command (e.g. check disk, read a log, install a package, verify a condition).
	  After the command runs, the system feeds its output back to you.

	- {"action": "ask", "result": "<your question to the user>", "comment": "<why you need this information>"}
	  When you need more information from the user (e.g. which port, what domain, what credentials).
	  The user's answer will be fed back to you.

	- {"action": "done", "result": "<summary of what was accomplished>", "comment": "<short overall assessment>"}
	  When the original goal has been fully achieved. The loop stops and this message is shown to the user.

	- {"action": "info", "result": "<your message to the user>", "comment": "<why you're showing this>"}
	  When you want to explain something, show progress, or share an observation without running a command.
	  If you desire users' further instruction, use "ask" not "info".
	  The loop continues after this, if the output seems cover user's req, use "done", not "info".

	- {"action": "giveup", "result": "<reason why you cannot proceed>", "comment": "<why it's not feasible to continue>"}
	  When the goal is impossible, too complex, or requires information unavailable to you. The loop stops.

	ACTION RULES:
	- One action per response. No markdown, no backticks, no text outside the JSON.
	- For "cmd", drive toward the user's goal step by step. Check preconditions before acting.
	- For "cmd", don't be too greedy; Generate short cmd, to get info step-by-step
	- If shell cmd is hard to realize req, can try python scripts, e.g. (python3 -c "...").
	- Remember you can check available cli tools by checking $PATH, if your some attempts failed.
	- Do NOT hallucinate command output. Trust only what the system feeds back.
	- If a command fails, analyze the error and try an alternative approach, or ask the user for help.
	- Use multiple "cmd" actions as needed — each turn is a chance to inspect, verify, or make progress.
	- When you've gathered enough evidence that the goal is met, respond with "done".
	- If stuck after several attempts, respond with "giveup", or ask for user's help.

	EXAMPLES:

	USER: sum numbers in 2nd column of data.csv
	YOU: {"action": "cmd", "result": "awk -F',' '{sum+=$2} END {print sum}' data.csv", "comment": "Calculates the sum of values in the second column of a comma-separated file."}
	[system feeds back the output of the command]
	YOU: {"action": "done", "result": "The sum is <calculated sum>.", "comment": "Result calculated, nothing to do"}

	USER: deploy my app
	YOU: {"action": "ask", "result": "What is the path to your app or repository?", "comment": "need the path to inspect build files"}
	[user answers "/home/user/myapp"]
	YOU: {"action": "cmd", "result": "ls /home/user/myapp", "comment": "Use ls to see check basic info of this proj"}
	...
	`,
}
