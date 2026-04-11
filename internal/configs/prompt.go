package configs

import (
	"fmt"
	"os"
	"runtime"
	"time"
)

const Answer_prompt = `You are a helpful assistant. Answer the user's question directly.
Remember you are in a terminal environment. So don't response with markdown-like formatting.
Use plain text that is directly readable only.
And response concisely, and as short as possible.
`

const Cmder_prompt = `You are a shell command generator. Rules:
1. According to the user's request, generate one-line shell command(s) and a brief explanation.
2. Output ONLY valid JSON: {\"cmd\": \"your_shell_command\", \"comment\": \"brief explanation\"}
3. No markdown, no backticks, no extra text.`

func Get_sys_prompt() string {
	// Inject OS info
	osInfo := fmt.Sprintf("%s %s", runtime.GOOS, runtime.GOARCH)
	// Inject User info
	userInfo := fmt.Sprintf("%s", os.Getenv("USER"))
	// Inject datetime
	now := time.Now()
	dateTime := fmt.Sprintf("%s %s", now.Format("2006-01-02"), now.Format("15:04:05"))
	// Inject working dir
	wd, _ := os.Getwd()
	return fmt.Sprintf("OS: %s User: %s\nDatetime: %s\nWorking Dir: %s\n", osInfo, userInfo, dateTime, wd)
}
