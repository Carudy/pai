package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"pai/internal/config"
	"pai/internal/tool"
	"pai/internal/ui"
)

type DevOpsRes struct {
	Action  string `json:"action"`
	Result  string `json:"result"`
	Comment string `json:"comment"`
}

func DevOps(ctx context.Context, cfg *config.UserConfig, userInput string) error {
	sysPrompt := BuildAgentPrompt(cfg.Prompts["devops"], "devops")

	history := []anyllm.Message{
		{Role: anyllm.RoleSystem, Content: sysPrompt},
		{Role: anyllm.RoleUser, Content: userInput},
	}

	iterations := 0
	const maxIterations = 100

	// main loop
	for {
		if iterations >= maxIterations {
			fmt.Printf("%s\n", ui.Styles["Warn"].Render("⚠️  Reached maximum iteration limit. Stopping."))
			return nil
		}
		iterations++

		// ── 1. Get the LLM's decision ──────────────────────────────────
		fmt.Printf("🤖 Processing...\n")
		content, newHistory, err := chat(ctx, cfg, cfg.Clients["devops"], history)
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
		if dec.Comment != "" {
			fmt.Printf("💭 %s\n", ui.Styles["Info"].Render(dec.Comment))
		}

		// ── 3. Execute the decision ────────────────────────────────────
		switch dec.Action {
		case "done":
			fmt.Printf("%s %s\n",
				ui.Styles["Success"].Render("✅ Done:"),
				ui.Styles["Info"].Render(dec.Result))
			return nil

		case "giveup":
			fmt.Printf("%s %s\n",
				ui.Styles["Warn"].Render("⚠️  Giving up:"),
				ui.Styles["Info"].Render(dec.Result))
			return nil

		case "info":
			fmt.Printf("ℹ️  %s\n", ui.Styles["Content"].Render(dec.Result))

		case "cmd":
			cmdRes, err := GenCMD(ctx, cfg, dec.Result)
			if err != nil {
				return fmt.Errorf("devops → cmd generation failed: %w", err)
			}

			fmt.Printf("%s\n", ui.Styles["Title"].Render("💡 Agent wants to execute command:"))
			fmt.Printf("    💬 %s\n", ui.Styles["Info"].Render(cmdRes.Comment))
			fmt.Printf("    $ %s\n", ui.Styles["Cmd"].Render(cmdRes.Cmd))

			output, execErr := tool.ExecuteCommand(os.Stdout, cmdRes.Cmd, true)
			if execErr != nil {
				fmt.Printf("%s\n", ui.Styles["Warn"].Render("⚠️  Command failed."))
			} else if output == "[user cancelled execution]" {
				fmt.Printf("%s\n", ui.Styles["Info"].Render("Execution skipped by user."))
			} else {
				fmt.Printf("%s\n", ui.Styles["Success"].Render("✅ Command succeeded."))
				fmt.Printf("📄 Output:\n%s\n", ui.Styles["Cmd"].Render(output))
			}

			observation := fmt.Sprintf(
				"COMMAND: %s\nEXIT_ERROR: %v\nOUTPUT:\n%s",
				cmdRes.Cmd, execErr, TruncateOutput(output, 2000),
			)
			history = append(history, anyllm.Message{
				Role:    anyllm.RoleUser,
				Content: "[cmd result]\n" + observation,
			})

		case "ask":
			fmt.Printf("%s %s\n",
				ui.Styles["Warn"].Render("🤔 Agent asks:"),
				ui.Styles["Content"].Render(dec.Result))

			answer, err := ui.GetUserTextInput("Your answer:")
			if err != nil {
				return fmt.Errorf("user input error: %w", err)
			}
			if answer == "" {
				answer = "[user cancelled / no answer]"
			}
			history = append(history, anyllm.Message{
				Role:    anyllm.RoleUser,
				Content: "[user answer]\n" + answer,
			})

		default:
			return fmt.Errorf("unknown devops action %q", dec.Action)
		}
	}
}
