package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/hq"
	"github.com/Carudy/pai/internal/llm"
	"github.com/Carudy/pai/internal/tool"
	"github.com/Carudy/pai/internal/ui"
)

func init() { Register(&DevopsAgent{}) }

// DevopsAgent runs autonomous reason–act–observe loops for multi-step tasks.
type DevopsAgent struct{}

func (a *DevopsAgent) Name() string { return "devops" }
func (a *DevopsAgent) Description() string {
	return "Autonomous reason–act–observe loop for multi-step sysadmin tasks"
}

func singleDevOpsLoop(
	ctx context.Context,
	cfg *config.UserConfig,
	history []llm.Message,
	log *hq.Logger) (bool, []llm.Message, error) {

	content, newHistory, usage, err := chatStr(ctx, cfg, cfg.Clients["devops"], history)
	if err != nil {
		return false, nil, err
	}
	history = newHistory

	log.Debugf("[AI Output]:\n%s\n", content)

	// Display token usage in a muted, comment-like style.
	if usage != nil {
		fmt.Printf("%s\n",
			ui.RenderStr("Token", fmt.Sprintf("[token: %s in, %s out, %s total]",
				llm.FormatTokens(usage.PromptTokens),
				llm.FormatTokens(usage.CompletionTokens),
				llm.FormatTokens(usage.Total()))))
	}

	resp, history, err := parseResponseWithRetry(ctx, cfg, cfg.Clients["devops"], content, history)
	if err != nil {
		return false, nil, err
	}

	// Actions that already print their own reason inline (in the [CMD 💬] / [RMT 💬] /
	// [PAI ✅] lines) should not have a redundant "agent says" prefix.
	selfExplaining := map[ActionType]bool{
		ActionExecute: true,
		ActionRemote:  true,
		ActionDone:    true,
	}
	if resp.Reason != "" && !selfExplaining[resp.Action] {
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagAgent", "[PAI 🤖]"),
			ui.RenderStr("Info", resp.Reason),
		)
	}

	log.Debugf("[Action]: %s\n[Reason]: %s\n", resp.Action, resp.Reason)

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

		output, execErr := tool.ExecuteCommand(cmd, true, os.Stdout)
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
		}

		observation := fmt.Sprintf(
			"COMMAND: %s\nEXIT_ERROR: %v\nOUTPUT:\n%s",
			cmd, execErr, TruncateOutput(output.String(), 2000),
		)
		history = append(history, llm.Message{
			Role:    llm.RoleUser,
			Content: "[cmd result]\n" + observation,
		})

	case ActionRemote:
		rp, err := resp.GetRemotePayload()
		if err != nil {
			return false, nil, fmt.Errorf("remote payload: %w", err)
		}
		if cfg.RemoteManager == nil {
			rm, err := tool.NewRemoteManager()
			if err != nil {
				return false, nil, fmt.Errorf("init remote sessions: %w", err)
			}
			cfg.RemoteManager = rm
		}

		fmt.Printf("%s %s\n",
			ui.RenderStr("TagAgent", "[RMT 💬]"),
			ui.RenderStr("Help", resp.Reason),
		)
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagExec", fmt.Sprintf("[RMT 💻 @%s]", rp.Host)),
			ui.RenderStr("Info", rp.Cmd),
		)

		output, execErr := cfg.RemoteManager.ExecuteRemote(ctx, rp, true, os.Stdout)
		if execErr != nil {
			fmt.Printf("%s ❌ %s\n%s\n",
				ui.RenderStr("TagSystem", "[SYS]"),
				ui.RenderStr("Warn", fmt.Sprintf("Remote command failed: %v", execErr)),
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
				ui.RenderStr("Success", "Remote command succeeded"),
			)
		}

		observation := fmt.Sprintf(
			"REMOTE HOST: %s\nCOMMAND: %s\nEXIT_ERROR: %v\nOUTPUT:\n%s",
			rp.Host, rp.Cmd, execErr, TruncateOutput(output.String(), 2000),
		)
		history = append(history, llm.Message{
			Role:    llm.RoleUser,
			Content: "[remote result]\n" + observation,
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

func (a *DevopsAgent) Run(ctx context.Context, cfg *config.UserConfig, userInput string) error {
	log := cfg.Logger

	agentPrompt, err := LoadAgentPrompt("devops", cfg.CustomPrompt)
	if err != nil {
		return fmt.Errorf("failed to load devops prompt: %w", err)
	}

	log.Debugf("Agent prompt: %s\n", agentPrompt)

	history := []llm.Message{
		{Role: llm.RoleSystem, Content: agentPrompt},
		{Role: llm.RoleUser, Content: userInput},
	}

	for {
		nextLoop, newHistory, err := singleDevOpsLoop(ctx, cfg, history, log)
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
