package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const apiURL = "https://api.deepseek.com/chat/completions"

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Request struct {
	Model       string    `json:"model"`
	Temperature float64   `json:"temperature"`
	Messages    []Message `json:"messages"`
}

type APIResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type CmdResult struct {
	Cmd     string `json:"cmd"`
	Comment string `json:"comment"`
}

func getSysPrompt() string {
	homeDir, _ := os.UserHomeDir()
	configPath := filepath.Join(homeDir, ".config", "aicmd.txt")

	var prompt string
	if content, err := os.ReadFile(configPath); err == nil {
		prompt = string(content)
	} else {
		prompt = `You are a shell command generator for {{OS}}. Rules:
1. Output ONLY valid JSON: {"cmd": "your_shell_command", "comment": "brief explanation"}
2. No markdown, no backticks, no extra text.`
	}

	// Inject OS information
	osInfo := fmt.Sprintf("%s %s", runtime.GOOS, runtime.GOARCH)
	return strings.ReplaceAll(prompt, "{{OS}}", osInfo)
}

func extractJSON(content string) (string, error) {
	// Robust extraction: find the first '{' and last '}'
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start == -1 || end == -1 || end < start {
		return "", fmt.Errorf("no JSON found in AI response")
	}
	return content[start : end+1], nil
}

func main() {
	apiKey := os.Getenv("DEEPSEEK_AI_CMD_TOOL_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "❌ Missing API key (DEEPSEEK_AI_CMD_TOOL_KEY)")
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		fmt.Println("Usage: ai <your request>")
		os.Exit(1)
	}

	userPrompt := strings.Join(os.Args[1:], " ")
	fmt.Fprint(os.Stderr, "🤖 Thinking...\r")

	reqBody := Request{
		Model:       "deepseek-chat",
		Temperature: 0,
		Messages: []Message{
			{Role: "system", Content: getSysPrompt()},
			{Role: "user", Content: userPrompt},
		},
	}

	jsonData, _ := json.Marshal(reqBody)
	client := &http.Client{Timeout: 30 * time.Second}

	req, _ := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Network error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 1MB Limit

	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "❌ API Error (%d): %s\n", resp.StatusCode, string(body))
		os.Exit(1)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil || len(apiResp.Choices) == 0 {
		fmt.Fprintln(os.Stderr, "❌ Invalid API response structure")
		os.Exit(1)
	}

	jsonStr, err := extractJSON(apiResp.Choices[0].Message.Content)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ AI Format Error: %s\n", apiResp.Choices[0].Message.Content)
		os.Exit(1)
	}

	var result CmdResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		fmt.Fprintln(os.Stderr, "❌ Failed to parse command JSON")
		os.Exit(1)
	}

	// Final Output UI
	fmt.Fprint(os.Stderr, "\033[2K\r") // Clear "Thinking..."
	if result.Comment != "" {
		fmt.Printf("\033[94m💡 %s\033[0m\n", result.Comment)
	}
	fmt.Printf("\033[1;32m> %s\033[0m\n\n", result.Cmd)

	// Interactive Prompt
	fmt.Fprint(os.Stderr, "Execute? [y/N]: ")
	var confirm string
	fmt.Scanln(&confirm)

	if strings.ToLower(confirm) == "y" {
		cmd := exec.Command("sh", "-c", result.Cmd)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "\n❌ Command failed: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Fprintln(os.Stderr, "👋 Cancelled.")
	}
}
