package agent

import (
	"context"
	"fmt"
	"os"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/llm"
	"github.com/Carudy/pai/internal/tool"
	"github.com/Carudy/pai/internal/ui"
)

func init() { Register(&PrivateAgent{}) }

type PrivateAgent struct{}

func (a *PrivateAgent) Name() string { return "private" }
func (a *PrivateAgent) Description() string {
	return "Extract math calculations using tools to calc"
}

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

	cmd := resp.GetPayload()

	fmt.Printf("%s %s\n", ui.RenderStr("TagExec", "[CMD 💬]"), ui.RenderStr("Help", resp.Reason))
	calc_cmd := "Calculation formula: \"" + cmd + "\""
	fmt.Printf("%s %s\n", ui.RenderStr("TagExec", "[CMD 💻]"), ui.RenderStr("Info", calc_cmd))
	unmask_cmd := tool.ReplaceMasks(cmd, config.MaskDB)
	fmt.Printf("%s %s\n", ui.RenderStr("TagExec", "[CMD 💻]"), ui.RenderStr("Info", "Unmasked formula: \""+unmask_cmd+"\""))

	if resp.Action == "execute" {
		cmd = "python3 -c \"print(" + tool.ReplaceMasks(cmd, config.MaskDB) + ")\""

		output, execErr := tool.ExecuteCommand(cmd, !tool.IsTrusted(cmd, cfg.TrustedCmds), os.Stdout)
		if execErr != nil {
			return fmt.Errorf("Execution Error: %v", execErr)
		}
		if output.Output != tool.CancelledOutput {
			fmt.Printf("%s ✅ %s\n", ui.RenderStr("TagSystem", "[SYS]"), ui.RenderStr("Success", "Command succeeded"))
			fmt.Printf("%s\n%s\n", ui.RenderStr("TagResult", "[RES]"), ui.RenderStr("Cmd", output.String()))
		} else {
			fmt.Printf("%s ⏭️ %s\n", ui.RenderStr("TagSystem", "[SYS]"), ui.RenderStr("Subdued", "Skipped"))
		}
	}

	return nil
}
