// Package role runs PAI's single agent loop against a data-defined role.
//
// There is one loop; roles differ only by data (see internal/prompts). Adding a
// role means adding a TOML file, not Go code.
package role

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/provider"
	"github.com/Carudy/pai/internal/tool"
	"github.com/Carudy/pai/internal/ui"
)

// Session is the runtime state for a single role run.
//
// It is deliberately kept separate from config.UserConfig (which holds
// configuration only) so that config never has to depend on the LLM client, the
// logger, or the SSH layer.
type Session struct {
	Client      provider.Provider
	Logger      *ui.Logger
	Interactive bool
	Remote      *tool.RemoteManager // lazily created by the remote tool
}

// Run loads the configured role and drives its reason–act–observe loop.
func Run(ctx context.Context, cfg *config.UserConfig, sess *Session, userInput string) error {
	names := prompts.RoleNames()
	if !slices.Contains(names, cfg.DefaultRole) {
		return fmt.Errorf("unknown role %q; available roles: %s (add your own in %s)",
			cfg.DefaultRole, strings.Join(names, ", "), prompts.UserRolesDir())
	}

	rp, err := chat.LoadRolePrompt(cfg.DefaultRole, cfg.CustomPrompt)
	if err != nil {
		return fmt.Errorf("load role %q: %w", cfg.DefaultRole, err)
	}
	if err := checkToolCoverage(rp); err != nil {
		return err
	}

	return loop(ctx, cfg, sess, rp, userInput)
}

func loop(ctx context.Context, cfg *config.UserConfig, sess *Session, rp *chat.RolePrompt, userInput string) error {
	log := sess.Logger
	log.Debugf("Role: %s\nIntro:\n%s\n", rp.Name, rp.Intro)

	// history holds only conversation turns; the role prompt is composed around
	// it on every request.
	var history []provider.Message
	if userInput != "" {
		history = append(history, provider.Message{Role: provider.RoleUser, Content: userInput})
	}

	for {
		// Bail out promptly on cancellation (e.g. SIGINT).
		if err := ctx.Err(); err != nil {
			return err
		}

		next, newHistory, err := step(ctx, cfg, sess, rp, history)
		if err != nil {
			return err
		}

		if next {
			history = newHistory
		} else if sess.Interactive {
			if err := ctx.Err(); err != nil {
				return err
			}

			fmt.Printf("%s %s\n",
				ui.RenderStr("TagAgent", "[PAI]"),
				ui.RenderStr("Info", "[Awaiting for new instructions.]"),
			)

			input, err := ui.GetUserTextInput("Input:")
			if err != nil {
				return fmt.Errorf("user input error: %w", err)
			}
			if input == "" {
				return nil
			}
			fmt.Printf("%s %s\n", ui.RenderStr("TagUser", "[User]"), ui.RenderStr("Info", input))
			history = append(history, provider.Message{Role: provider.RoleUser, Content: input})
		} else {
			return nil
		}

		fmt.Printf("%s\n", ui.Styles["Separator"].Render(strings.Repeat("─", 40)))
	}
}

// step runs one reason–act–observe turn. It returns next=false when the loop
// should stop (done/terminate, or a non-interactive finish).
func step(
	ctx context.Context,
	cfg *config.UserConfig,
	sess *Session,
	rp *chat.RolePrompt,
	history []provider.Message) (bool, []provider.Message, error) {

	log := sess.Logger

	content, newHistory, usage, err := chat.ChatStr(ctx, cfg, sess.Client, rp, history)
	if err != nil {
		return false, nil, err
	}
	history = newHistory

	log.Debugf("[AI Output]:\n%s\n", content)

	// Display token usage in a muted, comment-like style.
	if usage != nil {
		fmt.Printf("%s\n",
			ui.RenderStr("Token", fmt.Sprintf("[token: %s in, %s out, %s total]",
				provider.FormatTokens(usage.PromptTokens),
				provider.FormatTokens(usage.CompletionTokens),
				provider.FormatTokens(usage.Total()))))
	}

	resp, history, err := chat.ParseResponseWithRetry(ctx, cfg, sess.Client, rp, sess.Logger, content, history)
	if err != nil {
		return false, nil, err
	}

	// For tool/done the reason is shown alongside the action itself, so printing
	// it separately would be redundant.
	selfExplaining := map[chat.ActionType]bool{
		chat.ActionTool: true,
		chat.ActionDone: true,
	}
	if resp.Reason != "" && !selfExplaining[resp.Action] {
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagAgent", "[PAI 🤖]"),
			ui.RenderStr("Info", resp.Reason),
		)
	}

	log.Debugf("[Action]: %s\n[Reason]: %s\n", resp.Action, resp.Reason)

	switch resp.Action {
	case chat.ActionDone:
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagAgent", "[PAI ✅]"),
			ui.RenderStr("Success", resp.GetPayload()),
		)
		return false, history, nil

	case chat.ActionTerminate:
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagAgent", "[PAI 💔]"),
			ui.RenderStr("Warn", resp.GetPayload()),
		)
		return false, history, nil

	case chat.ActionAsk:
		question := resp.GetPayload()
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagAgent", "[PAI 🙋]"),
			ui.RenderStr("Warn", question),
		)

		answer, err := ui.GetUserTextInput("Your answer:")
		if err != nil {
			return false, nil, fmt.Errorf("user input error: %w", err)
		}
		if answer != "" {
			fmt.Printf("%s %s\n", ui.RenderStr("TagUser", "[User]"), ui.RenderStr("Info", answer))
		} else {
			answer = "[user cancelled / no answer]"
		}
		history = append(history, provider.Message{
			Role:    provider.RoleUser,
			Content: "[user answer]\n" + answer,
		})

	case chat.ActionTool:
		tp, err := resp.GetToolPayload()
		if err != nil {
			return false, nil, err
		}
		log.Debugf("toolname: %s\n", tp.ToolName)

		observation, err := invokeTool(ctx, cfg, sess, rp, tp, resp.Reason)
		if err != nil {
			return false, nil, err
		}
		history = append(history, provider.Message{
			Role:    provider.RoleUser,
			Content: observation,
		})

	default:
		return false, nil, fmt.Errorf("unknown action %q", resp.Action)
	}

	return true, history, nil
}

// invokeTool dispatches a tool call and returns the observation to feed back to
// the model. Bad calls (tool not enabled for this role, malformed payload) are
// returned as observations rather than errors, so the agent can correct itself
// instead of the session aborting.
func invokeTool(
	ctx context.Context,
	cfg *config.UserConfig,
	sess *Session,
	rp *chat.RolePrompt,
	tp chat.ToolPayload,
	reason string) (string, error) {

	if !rp.HasTool(tp.ToolName) {
		return fmt.Sprintf("[tool error]\nTOOL: %s\nERROR: tool is not available to this role; available tools: %s",
			tp.ToolName, strings.Join(rp.ToolNames(), ", ")), nil
	}

	handler, ok := toolHandlers[tp.ToolName]
	if !ok {
		return "", fmt.Errorf("tool %q has no handler", tp.ToolName)
	}

	observation, err := handler(ctx, cfg, sess, reason, tp.Payload)
	if err != nil {
		return fmt.Sprintf("[tool error]\nTOOL: %s\nERROR: %v", tp.ToolName, err), nil
	}
	return observation, nil
}
