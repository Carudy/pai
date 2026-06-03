package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/llm"
	"github.com/Carudy/pai/internal/tool"
	"github.com/Carudy/pai/internal/ui"
)

func init() { Register(&PrivateAgent{}) }

// PrivateAgent extracts masked math expressions, unmasks them, and computes
// results via Python.  Numbers in user input are masked as <mask:TOKEN> and
// resolved from ~/.config/pai/mask.yml.
type PrivateAgent struct{}

func (a *PrivateAgent) Name() string        { return "private" }
func (a *PrivateAgent) Description() string { return "Compute masked math expressions privately" }

func (a *PrivateAgent) Run(ctx context.Context, cfg *config.UserConfig, userInput string) error {
	sysPrompt, err := LoadAgentPrompt("private", cfg.CustomPrompt)
	if err != nil {
		return fmt.Errorf("failed to load private prompt: %w", err)
	}

	history := []llm.Message{
		{Role: llm.RoleSystem, Content: sysPrompt},
		{Role: llm.RoleUser, Content: userInput},
	}

	content, history, _, err := chatStr(ctx, cfg, cfg.Clients["private"], history)
	if err != nil {
		return err
	}

	resp, _, err := parseResponseWithRetry(ctx, cfg, cfg.Clients["private"], content, history)
	if err != nil {
		return err
	}

	switch resp.Action {
	case ActionTool:
		tp, err := resp.GetToolPayload()
		if err != nil {
			return err
		}
		if tp.ToolName != "execute" {
			return fmt.Errorf("private agent: unknown toolname %q", tp.ToolName)
		}

		var formula string
		if err := json.Unmarshal(tp.Payload, &formula); err != nil {
			return fmt.Errorf("execute payload: %w", err)
		}

		// Show the masked formula (before substitution).
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagAgent", "[CALC 💬]"),
			ui.RenderStr("Help", resp.Reason),
		)
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagExec", "[CALC 📐]"),
			ui.RenderStr("Info", formula),
		)

		// Substitute masks with real values.
		unmasked := tool.ReplaceMasks(formula, config.MaskDB)
		if unmasked != formula {
			fmt.Printf("%s %s\n",
				ui.RenderStr("Subdued", "  →"),
				ui.RenderStr("Subdued", unmasked),
			)
		}

		// Execute via Python.
		cmd := "python3 -c \"print(" + unmasked + ")\""
		output, execErr := tool.ExecuteCommand(cmd, true, nil)
		if execErr != nil {
			return fmt.Errorf("execution error: %w", execErr)
		}
		if output.Output == tool.CancelledOutput {
			fmt.Printf("%s %s\n",
				ui.RenderStr("TagSystem", "[SYS]"),
				ui.RenderStr("Subdued", "Skipped"),
			)
		} else {
			fmt.Printf("%s %s\n",
				ui.RenderStr("TagResult", "[RES]"),
				ui.RenderStr("Success", output.Output),
			)
		}

	case ActionInfo:
		answer := resp.GetPayload()
		fmt.Printf("%s %s\n%s\n",
			ui.RenderStr("TagAgent", "[PAI 🤖]"),
			ui.RenderStr("Help", resp.Reason),
			ui.RenderStr("Content", answer),
		)

	default:
		return fmt.Errorf("private agent: unexpected action %q", resp.Action)
	}

	return nil
}
