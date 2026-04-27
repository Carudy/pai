package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"pai/internal/llm"

	"pai/internal/config"
	"pai/internal/tool"
	"pai/internal/ui"
)

// maxHistoryBytes caps the cumulative history payload sent to the LLM.
// When exceeded, older messages (beyond the first N) are dropped to keep
// requests fast and within context windows.
const maxHistoryBytes = 32_000

type DevOpsRes struct {
	Action  string `json:"action"`
	Result  string `json:"result"`
	Comment string `json:"comment"`
}

// trimHistory removes old messages when the total content exceeds
// maxHistoryBytes, always keeping the system prompt and the most recent
// messages.
func trimHistory(history []llm.Message) []llm.Message {
	contentLen := func(v any) int {
		if s, ok := v.(string); ok {
			return len(s)
		}
		return 0
	}

	total := 0
	for _, m := range history {
		total += contentLen(m.Content)
	}
	if total <= maxHistoryBytes {
		return history
	}

	// Always keep index 0 (system prompt).
	// Walk from the end forward to find how many fit.
	keep := 1 // system
	accum := contentLen(history[0].Content)
	for i := len(history) - 1; i > 0 && accum < maxHistoryBytes; i-- {
		accum += contentLen(history[i].Content)
		keep++
	}
	// keep is now the count of trailing messages we want.
	start := len(history) - (keep - 1) // minus the system message already counted
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

	// main loop
	for {
		if iterations >= maxIterations {
			fmt.Printf("%s %s\n",
				ui.Styles["TagSystem"].Render("[Sys]"),
				ui.Styles["Warn"].Render("⚠️ Reached maximum iteration limit. Stopping."))
			return nil
		}
		iterations++

		// ── Turn separator ─────────────────────────────────────────────
		if iterations > 1 {
			fmt.Printf("%s\n", ui.Styles["Separator"].Render(strings.Repeat("─", 40)))
		}

		// ── 1. Get the LLM's decision ──────────────────────────────────
		content, newHistory, err := chatStdout(ctx, cfg, cfg.Clients["devops"], history)
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
			return fmt.Errorf("failed to parse devops JSON: %w", err)
		}

		// ── 2. Show the agent's reasoning ──────────────────────────────
		if dec.Comment != "" && dec.Action != "cmd" {
			fmt.Printf("%s %s\n",
				ui.Styles["TagAgent"].Render("[PAI 🤖]"),
				ui.Styles["Info"].Render(dec.Comment))
		}

		// ── 3. Execute the decision ────────────────────────────────────
		switch dec.Action {
		case "done":
			fmt.Printf("%s %s\n",
				ui.Styles["TagAgent"].Render("[PAI ✅]"),
				ui.Styles["Success"].Render(dec.Result))
			return nil

		case "giveup":
			fmt.Printf("%s %s\n",
				ui.Styles["TagAgent"].Render("[PAI 💔]"),
				ui.Styles["Warn"].Render(dec.Result))
			return nil

		case "info":
			fmt.Printf("%s %s\n",
				ui.Styles["TagAgent"].Render("[PAI ℹ️]"),
				ui.Styles["Content"].Render(dec.Result))

		case "cmd":
			fmt.Printf("%s %s\n",
				ui.Styles["TagExec"].Render("[CMD 💬]"),
				ui.Styles["Help"].Render(dec.Comment))
			fmt.Printf("%s %s\n",
				ui.Styles["TagExec"].Render("[CMD 💻]"),
				ui.Styles["Info"].Render(dec.Result))

			output, execErr := tool.ExecuteCommand(os.Stdout, dec.Result, true)
			if execErr != nil {
				fmt.Printf("%s ❌ %s\n",
					ui.Styles["TagSystem"].Render("[SYS]"),
					ui.Styles["Warn"].Render("Command failed"))
				if output != "" {
					fmt.Printf("%s\n%s\n",
						ui.Styles["TagResult"].Render("[RES]"),
						ui.Styles["Warn"].Render(output))
				}
			} else if output == "[user cancelled execution]" {
				fmt.Printf("%s %s\n",
					ui.Styles["TagSystem"].Render("[SYS ⏭️]"),
					ui.Styles["Subdued"].Render("Skipped"))
			} else {
				fmt.Printf("%s %s\n",
					ui.Styles["TagSystem"].Render("[SYS ✅]"),
					ui.Styles["Success"].Render("Command succeeded"))
				if output != "" {
					fmt.Printf("%s\n%s\n",
						ui.Styles["TagResult"].Render("[RES]"),
						ui.Styles["Cmd"].Render(output))
				}
			}

			observation := fmt.Sprintf(
				"COMMAND: %s\nEXIT_ERROR: %v\nOUTPUT:\n%s",
				dec.Result, execErr, TruncateOutput(output, 2000),
			)
			history = append(history, llm.Message{
				Role:    llm.RoleUser,
				Content: "[cmd result]\n" + observation,
			})

		case "ask":
			fmt.Printf("%s %s\n",
				ui.Styles["TagAgent"].Render("[PAI 🙋]"),
				ui.Styles["Warn"].Render(dec.Result))

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
	}
}
