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
			ui.RenderStr("TagAgent", "[PAI 🤖]"),
			ui.RenderStr("Info", resp.Reason),
		)
	}

	config.DebugLog(os.Stdout, "[Action]: %s\n[Reason]: %s\n", resp.Action, resp.Reason)

	switch resp.Action {
	case ActionDone:
		info := resp.GetPayload()
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagAgent", "[PAI ✅]"),
			ui.RenderStr("Success", info),
		)
		return false, history, nil

	case ActionTerminate:
		info := resp.GetPayload()
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagAgent", "[PAI 💔]"),
			ui.RenderStr("Warn", info),
		)
		return false, history, nil

	case ActionInfo:
		info := resp.GetPayload()
		fmt.Printf("%s\n%s\n",
			ui.RenderStr("TagAgent", "[PAI ℹ️]"),
			ui.RenderStr("Content", info),
		)
		history = append(history,
			llm.Message{
				Role:    llm.RoleAssistant,
				Content: "[CMD RESULT]\n" + info,
			},
		)
		history = append(history,
			llm.Message{
				Role:    llm.RoleUser,
				Content: "ok",
			},
		)

	case ActionExecute:
		cmd := resp.GetPayload()
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagAgent", "[CMD 💬]"),
			ui.RenderStr("Help", resp.Reason),
		)
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagExec", "[CMD 💻]"),
			ui.RenderStr("Info", cmd),
		)

		output, execErr := tool.ExecuteCommand(cmd, true)
		if execErr != nil {
			fmt.Printf("%s ❌ %s\n%s\n",
				ui.RenderStr("TagSystem", "[SYS]"),
				ui.RenderStr("Warn", "Command failed"),
				ui.RenderStr("Warn", output.Output),
			)
		} else if output.Output == tool.CancelledOutput {
			fmt.Printf("%s %s\n",
				ui.RenderStr("TagSystem", "[SYS]"),
				ui.RenderStr("Subdued", "Skipped"),
			)
		} else {
			fmt.Printf("%s %s\n",
				ui.RenderStr("TagSystem", "[SYS]"),
				ui.RenderStr("Success", "Command succeeded"),
			)
			if output.Output != "" {
				fmt.Printf("%s\n%s\n",
					ui.RenderStr("TagResult", "[CMD Result]"),
					ui.RenderStr("ExeRes", output.Output),
				)
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
		q := resp.GetPayload()
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagAgent", "[PAI 🙋]"),
			ui.RenderStr("Warn", q),
		)

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

	agentPrompt, err := LoadAgentPrompt("devops", cfg.CustomPrompt)
	if err != nil {
		return fmt.Errorf("failed to load devops prompt: %w", err)
	}

	config.DebugLog(os.Stdout, "Agent prompt: %s\n", agentPrompt)

	history := []llm.Message{
		{Role: llm.RoleSystem, Content: agentPrompt},
		{Role: llm.RoleUser, Content: userInput},
	}

	for {
		nextLoop, newHistory, err := singleDevOpsLoop(ctx, cfg, history)
		if err != nil {
			return err
		}

		if nextLoop {
			history = newHistory
		} else if cfg.Flags.Inter {
			fmt.Printf("%s %s\n",
				ui.RenderStr("TagAgent", "[PAI]"),
				ui.RenderStr("Info", "[Awaiting for new instructions.]"),
			)

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
