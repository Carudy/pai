package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/llm"
	"github.com/Carudy/pai/internal/tool"
	"github.com/Carudy/pai/internal/ui"
)

// maxHistoryBytes caps the cumulative history payload sent to the LLM.
// When exceeded, older messages (beyond the first N) are dropped to keep
// requests fast and within context windows.
const maxHistoryBytes = 32_000

// DevOpsRes is the JSON envelope the LLM must return each turn.
// Result is stored as json.RawMessage so models that return a JSON object
// or array (instead of a plain string) don't cause a parse error.
type DevOpsRes struct {
	Action  string          `json:"action"`
	Result  json.RawMessage `json:"result"`
	Comment string          `json:"comment"`
}

func (d *DevOpsRes) ResultCMD() string {
	if len(d.Result) == 0 {
		return ""
	}

	var cmd string
	if err := json.Unmarshal(d.Result, &cmd); err == nil {
		return strings.TrimSpace(cmd)
	}

	var obj map[string]any
	if err := json.Unmarshal(d.Result, &obj); err == nil {
		// Priority list of common keys an AI might use
		keys := []string{"command", "cmd", "exec", "shell"}
		for _, k := range keys {
			if v, ok := obj[k].(string); ok {
				return strings.TrimSpace(v)
			}
		}
	}

	s := string(d.Result)
	return strings.Trim(s, "\" ")
}

func (d *DevOpsRes) ResultPretty() string {
	if len(d.Result) == 0 {
		return ""
	}

	// Case 1: try pretty-printing as a JSON object/array
	var indentBuffer bytes.Buffer
	if err := json.Indent(&indentBuffer, d.Result, "", "  "); err == nil {
		// Only use indented JSON if it's actually an object or array
		trimmed := bytes.TrimSpace(d.Result)
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			return indentBuffer.String()
		}
	}

	// Case 2: try unquoting a JSON-encoded string (e.g. "foo\nbar")
	var str string
	if err := json.Unmarshal(d.Result, &str); err == nil {
		return strings.TrimSpace(str)
	}

	// Case 3: plain text fallback
	return strings.TrimSpace(string(d.Result))
}

func trimHistory(history []llm.Message) []llm.Message {
	total := 0
	for _, m := range history {
		total += len(m.Content)
	}
	if total <= maxHistoryBytes {
		return history
	}

	// Always keep index 0 (system prompt).
	// Walk from the end forward to find how many fit.
	keep := 1 // system
	accum := len(history[0].Content)
	for i := len(history) - 1; i > 0 && accum < maxHistoryBytes; i-- {
		accum += len(history[i].Content)
		keep++
	}
	start := len(history) - (keep - 1)
	if start < 1 {
		start = 1
	}
	trimmed := make([]llm.Message, 0, 1+keep)
	trimmed = append(trimmed, history[0])
	trimmed = append(trimmed, history[start:]...)
	return trimmed
}

func DevOps(ctx context.Context, cfg *config.UserConfig, userInput string) error {
	sysPrompt := BuildAgentPrompt(cfg.Prompts["devops"], "devops")

	history := []llm.Message{
		{Role: llm.RoleSystem, Content: sysPrompt},
		{Role: llm.RoleUser, Content: userInput},
	}

	iterations := 0
	const maxIterations = 100

	for {
		if iterations >= maxIterations {
			fmt.Printf("%s %s\n",
				ui.Styles["TagSystem"].Render("[Sys]"),
				ui.Styles["Warn"].Render("⚠️ Reached maximum iteration limit. Stopping."))
			return nil
		}
		iterations++

		// Turn separator.
		if iterations > 1 {
			fmt.Printf("%s\n", ui.Styles["Separator"].Render(strings.Repeat("─", 40)))
		}

		// ── 1. Get the LLM's decision ──────────────────────────────────
		content, newHistory, err := chatStr(ctx, cfg, cfg.Clients["devops"], history)
		if err != nil {
			return err
		}
		history = newHistory

		jsonStr, err := ExtractJSON(content)
		if err != nil {
			return fmt.Errorf("AI format error: %s", content)
		}

		var dec DevOpsRes
		if err := json.Unmarshal([]byte(jsonStr), &dec); err != nil {
			return fmt.Errorf("failed to parse devops JSON: %w\nraw: %s", err, jsonStr)
		}

		// ── 2. Show the agent's reasoning ──────────────────────────────
		if dec.Comment != "" && dec.Action != "cmd" && dec.Action != "done" {
			fmt.Printf("%s %s\n",
				ui.Styles["TagAgent"].Render("[PAI 🤖]"),
				ui.Styles["Info"].Render(dec.Comment))
		}

		// ── 3. Execute the decision ────────────────────────────────────
		switch dec.Action {
		case "done":
			fmt.Printf("%s %s\n",
				ui.Styles["TagAgent"].Render("[PAI ✅]"),
				ui.Styles["Success"].Render(dec.ResultPretty()))

			if cfg.Flags.Inter {
				fmt.Printf("%s %s\n",
					ui.Styles["TagAgent"].Render("[PAI]"),
					ui.Styles["Info"].Render("[Awaiting for new instructions.]"))

				input, err := ui.GetUserTextInput("Input:")
				if err != nil {
					return fmt.Errorf("user input error: %w", err)
				}
				if input == "" {
					return nil
				}
				history = append(history, llm.Message{
					Role:    llm.RoleUser,
					Content: input,
				})
			} else {
				return nil
			}

		case "giveup":
			fmt.Printf("%s %s\n",
				ui.Styles["TagAgent"].Render("[PAI 💔]"),
				ui.Styles["Warn"].Render(dec.ResultPretty()))

			if cfg.Flags.Inter {
				fmt.Printf("%s %s\n",
					ui.Styles["TagAgent"].Render("[PAI]"),
					ui.Styles["Info"].Render("[Awaiting for new instructions.]"))

				input, err := ui.GetUserTextInput("Input:")
				if err != nil {
					return fmt.Errorf("user input error: %w", err)
				}
				if input == "" {
					return nil
				}
				history = append(history, llm.Message{
					Role:    llm.RoleUser,
					Content: input,
				})
			} else {
				return nil
			}

		case "info":
			infoResult := dec.ResultPretty()

			fmt.Printf("%s %s\n",
				ui.Styles["TagAgent"].Render("[PAI ℹ️]"),
				ui.Styles["Content"].Render(infoResult))

			history = append(history, llm.Message{
				Role:    llm.RoleAssistant,
				Content: "[cmd result]\n" + infoResult,
			})
			if cfg.Provider == "mistral" {
				history = append(history, llm.Message{
					Role:    llm.RoleUser,
					Content: "got it",
				})
			}

		case "cmd":
			to_exe_cmd := dec.ResultCMD()
			fmt.Printf("%s %s\n",
				ui.Styles["TagExec"].Render("[CMD 💬]"),
				ui.Styles["Help"].Render(dec.Comment))
			fmt.Printf("%s %s\n",
				ui.Styles["TagExec"].Render("[CMD 💻]"),
				ui.Styles["Info"].Render(to_exe_cmd))

			output, execErr := tool.ExecuteCommand(to_exe_cmd, true)
			if execErr != nil {
				fmt.Printf("%s ❌ %s\n",
					ui.Styles["TagSystem"].Render("[SYS]"),
					ui.Styles["Warn"].Render("Command failed"))
				if output.Output != "" {
					fmt.Printf("%s\n%s\n",
						ui.Styles["TagResult"].Render("[❌ Error Info]"),
						ui.Styles["Warn"].Render(output.Output))
				}
			} else if output.Output == "[user cancelled execution]" {
				fmt.Printf("%s %s\n",
					ui.Styles["TagSystem"].Render("[SYS]"),
					ui.Styles["Subdued"].Render("Skipped"))
			} else {
				fmt.Printf("%s %s\n",
					ui.Styles["TagSystem"].Render("[SYS]"),
					ui.Styles["Success"].Render("Command succeeded"))
				if output.Output != "" {
					fmt.Printf("%s\n%s\n",
						ui.Styles["TagResult"].Render("[CMD Result]"),
						ui.Styles["ExeRes"].Render(output.Output))
				}
			}

			observation := fmt.Sprintf(
				"COMMAND: %s\nEXIT_ERROR: %v\nOUTPUT:\n%s",
				to_exe_cmd, execErr, TruncateOutput(output.String(), 2000),
			)
			history = append(history, llm.Message{
				Role:    llm.RoleUser,
				Content: "[cmd result]\n" + observation,
			})

		case "ask":
			fmt.Printf("%s %s\n",
				ui.Styles["TagAgent"].Render("[PAI 🙋]"),
				ui.Styles["Warn"].Render(dec.ResultPretty()))

			answer, err := ui.GetUserTextInput("Your answer:")
			if err != nil {
				return fmt.Errorf("user input error: %w", err)
			}
			if answer == "" {
				answer = "[user cancelled / no answer]"
			}
			history = append(history, llm.Message{
				Role:    llm.RoleUser,
				Content: "[user answer]\n" + answer,
			})

		default:
			return fmt.Errorf("unknown devops action %q", dec.Action)
		}

		// history = trimHistory(history)
	}
}
