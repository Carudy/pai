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

func init() { Register(&CmdAgent{}) }

// CmdAgent generates one-shot shell commands.
type CmdAgent struct{}

func (a *CmdAgent) Name() string { return "cmd" }
func (a *CmdAgent) Description() string {
	return "One-shot shell command generation with optional execution"
}

func (a *CmdAgent) Run(ctx context.Context, cfg *config.UserConfig, userInput string) error {
	sysPrompt, err := LoadAgentPrompt("cmd", cfg.CustomPrompt)
	if err != nil {
		return fmt.Errorf("failed to load cmd prompt: %w", err)
	}

	history := []llm.Message{
		{Role: llm.RoleSystem, Content: sysPrompt},
		{Role: llm.RoleUser, Content: userInput},
	}

	content, history, _, err := chatStr(ctx, cfg, cfg.Clients["cmd"], history)
	if err != nil {
		return err
	}

	resp, _, err := parseResponseWithRetry(ctx, cfg, cfg.Clients["cmd"], content, history)
	if err != nil {
		return err
	}

	cmd := resp.GetPayload()

	fmt.Printf("%s %s\n", ui.Styles["TagExec"].Render("[CMD 💬]"), ui.Styles["Help"].Render(resp.Reason))
	fmt.Printf("%s %s\n", ui.Styles["TagExec"].Render(fmt.Sprintf("[CMD 💻 %s]", tool.Shell())), ui.Styles["Info"].Render(cmd))

	output, execErr := tool.ExecuteCommand(cmd, !tool.IsTrusted(cmd, cfg.TrustedCmds), os.Stdout)
	if execErr != nil {
		return fmt.Errorf("Execution Error: %v", execErr)
	}
	if output.Output != tool.CancelledOutput {
		fmt.Printf("%s ✅ %s\n", ui.Styles["TagSystem"].Render("[SYS]"), ui.Styles["Success"].Render("Command succeeded"))
		fmt.Printf("%s\n%s\n", ui.Styles["TagResult"].Render("[RES]"), ui.Styles["Cmd"].Render(output.String()))
	} else {
		fmt.Printf("%s ⏭️ %s\n", ui.Styles["TagSystem"].Render("[SYS]"), ui.Styles["Subdued"].Render("Skipped"))
	}

	return nil
}
