package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/llm"
	"github.com/Carudy/pai/internal/tool"
	"github.com/Carudy/pai/internal/ui"
)

func singleDevOpsLoop(
	ctx context.Context,
	cfg *config.UserConfig,
	history []llm.Message) (bool, []llm.Message, error) {

	content, newHistory, err := chatStr(ctx, cfg, cfg.Clients["devops"], history)
	if err != nil {
		return false, nil, err
	}
	history = newHistory

	config.DebugLog(os.Stdout, "[AI Output]:\n%s\n", content)

	resp, history, err := parseResponseWithRetry(ctx, cfg, cfg.Clients["devops"], content, history)
	if err != nil {
		return false, nil, err
	}

	if resp.Reason != "" && resp.Action != ActionExecute && resp.Action != ActionDone {
		fmt.Printf("%s %s\n",
			ui.Styles["TagAgent"].Render("[PAI 🤖]"),
			ui.Styles["Info"].Render(resp.Reason))
	}

	config.DebugLog(os.Stdout, "[Action]: %s\n[Reason]: %s\n", resp.Action, resp.Reason)

	switch resp.Action {
	case ActionDone:
		info, _ := resp.GetPayload()
		fmt.Printf("%s %s\n",
			ui.Styles["TagAgent"].Render("[PAI ✅]"),
			ui.Styles["Success"].Render(info))
		return false, history, nil

	case ActionTerminate:
		info, _ := resp.GetPayload()
		fmt.Printf("%s %s\n",
			ui.Styles["TagAgent"].Render("[PAI 💔]"),
			ui.Styles["Warn"].Render(info))
		return false, history, nil

	case ActionInfo:
		info, _ := resp.GetPayload()
		fmt.Printf("%s %s\n",
			ui.Styles["TagAgent"].Render("[PAI ℹ️]"),
			ui.Styles["Content"].Render(info))

		history = append(history,
			llm.Message{
				Role:    llm.RoleAssistant,
				Content: "[cmd result]\n" + info,
			},
			llm.Message{
				Role:    llm.RoleUser,
				Content: "got it",
			},
		)

	case ActionExecute:
		cmd, _ := resp.GetPayload()
		fmt.Printf("%s %s\n",
			ui.Styles["TagExec"].Render("[CMD 💬]"),
			ui.Styles["Help"].Render(resp.Reason))
		fmt.Printf("%s %s\n",
			ui.Styles["TagExec"].Render("[CMD 💻]"),
			ui.Styles["Info"].Render(cmd))

		output, execErr := tool.ExecuteCommand(cmd, true)
		if execErr != nil {
			fmt.Printf("%s ❌ %s\n%s\n",
				ui.Styles["TagSystem"].Render("[SYS]"),
				ui.Styles["Warn"].Render("Command failed"),
				ui.Styles["Warn"].Render(output.Output))
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
			cmd, execErr, TruncateOutput(output.String(), 2000),
		)
		history = append(history, llm.Message{
			Role:    llm.RoleUser,
			Content: "[cmd result]\n" + observation,
		})

	case ActionAsk:
		q, _ := resp.GetPayload()
		fmt.Printf("%s %s\n",
			ui.Styles["TagAgent"].Render("[PAI 🙋]"),
			ui.Styles["Warn"].Render(q))

		answer, err := ui.GetUserTextInput("Your answer:")
		if err != nil {
			return false, nil, fmt.Errorf("user input error: %w", err)
		}
		if answer == "" {
			answer = "[user cancelled / no answer]"
		}
		history = append(history, llm.Message{
			Role:    llm.RoleUser,
			Content: "[user answer]\n" + answer,
		})

	default:
		return false, nil, fmt.Errorf("unknown devops action %q", resp.Action)
	}

	return true, history, nil
}

func DevOps(ctx context.Context, cfg *config.UserConfig, userInput string) error {

	agent_prompt, err := LoadAgentPrompt("devops", cfg.CustomPrompt)
	if err != nil {
		return fmt.Errorf("failed to load devops prompt: %w", err)
	}

	config.DebugLog(os.Stdout, "Agent prompt: %s\n", agent_prompt)

	history := []llm.Message{
		{Role: llm.RoleSystem, Content: agent_prompt},
		{Role: llm.RoleUser, Content: userInput},
	}

	for {
		next_loop, new_history, err := singleDevOpsLoop(ctx, cfg, history)
		if err != nil {
			return err
		}

		if next_loop {
			history = new_history
		} else if cfg.Flags.Inter {
			fmt.Printf("%s %s\n",
				ui.Styles["TagAgent"].Render("[PAI]"),
				ui.Styles["Info"].Render("[Awaiting for new instructions.]"))

			input, err := ui.GetUserTextInput("Input:")
			if err != nil {
				return fmt.Errorf("user input error: %w", err)
			}
			if input == "" {
				return fmt.Errorf("user empty input")
			}
			history = append(history, llm.Message{
				Role:    llm.RoleUser,
				Content: input,
			})
		} else {
			return nil
		}
		fmt.Printf("%s\n", ui.Styles["Separator"].Render(strings.Repeat("─", 40)))
	}
}
