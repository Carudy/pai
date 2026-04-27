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

// DevOpsRes is the unified JSON shape the DevOps LLM returns each iteration.
type DevOpsRes struct {
	Action string `json:"action"`
	Result string `json:"result"`
}

// DevOps runs a continuous reason–act–observe loop:
//  1. Feed the conversation (system + user + assistant history) to the LLM.
//  2. LLM returns one of:  cmd | ask | info | done | giveup
//  3. Act on the decision and feed the result back into the conversation.
//  4. Repeat until done/giveup.
func DevOps(ctx context.Context, cfg *config.UserConfig, userInput string) error {
	sysPrompt := BuildAgentPrompt(cfg.Prompts["devops"], "devops")

	history := []anyllm.Message{
		{Role: anyllm.RoleSystem, Content: sysPrompt},
		{Role: anyllm.RoleUser, Content: userInput},
	}

	iterations := 0
	const maxIterations = 50

	for {
		fmt.Printf("🤖 Processing...\n")
		if iterations >= maxIterations {
			fmt.Printf("%s\n", ui.Styles["Warn"].Render("⚠️  Reached maximum iteration limit. Stopping."))
			return nil
		}
		iterations++

		// ── 1. Get the LLM's decision ──────────────────────────────────
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

		// ── 2. Execute the decision ────────────────────────────────────
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
			fmt.Printf("%s\n%s\n",
				ui.Styles["Title"].Render("ℹ️  "+dec.Result),
				ui.Styles["Debug"].Render("(continuing…)"))

		case "cmd":
			cmdRes, err := GenCMD(ctx, cfg, dec.Result)
			if err != nil {
				return fmt.Errorf("devops → cmd generation failed: %w", err)
			}

			fmt.Printf("%s\n", ui.Styles["Title"].Render("💡 Agent wanna exec:"))
			fmt.Printf("    TIP: %s\n", ui.Styles["Info"].Render(cmdRes.Comment))
			fmt.Printf("    CMD: %s\n", ui.Styles["Cmd"].Render(cmdRes.Cmd))

			output, execErr := tool.ExecuteCommand(os.Stdout, cmdRes.Cmd, true)
			if execErr != nil {
				fmt.Printf("%s\n", ui.Styles["Warn"].Render("⚠️  Command failed."))
			}
			if output != "[user cancelled execution]" {
				fmt.Printf("%s\n", ui.Styles["Success"].Render("✅ Command succeeded."))
				fmt.Printf("Exec Res:\n%s\n", ui.Styles["Cmd"].Render(output))
			} else {
				fmt.Printf("%s\n", ui.Styles["Info"].Render("Execution skipped by user."))
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
