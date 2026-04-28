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

type CmdResult struct {
	Cmd     string `json:"cmd"`
	Comment string `json:"comment"`
}

func GenCMD(ctx context.Context, cfg *config.UserConfig, user_input string) error {

	sys_prompt := BuildAgentPrompt(cfg.Prompts["cmd"], "cmd")
	var history = []llm.Message{
		{Role: llm.RoleSystem, Content: sys_prompt},
		{Role: llm.RoleUser, Content: user_input},
	}

	content, _, err := chatStdout(ctx, cfg, cfg.Clients["cmd"], history)
	if err != nil {
		return err
	}

	jsonStr, err := ExtractJSON(content)
	if err != nil {
		return fmt.Errorf("AI format error: %s", content)
	}

	var result CmdResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return fmt.Errorf("failed to parse command JSON: %w", err)
	}

	fmt.Printf("%s %s\n", ui.Styles["TagExec"].Render("[CMD 💬]"), ui.Styles["Help"].Render(result.Comment))
	fmt.Printf("%s %s\n", ui.Styles["TagExec"].Render("[CMD 💻]"), ui.Styles["Info"].Render(result.Cmd))

	output, execErr := tool.ExecuteCommand(result.Cmd, true)
	if execErr != nil {
		return fmt.Errorf("Execution Error: %v", execErr)
	}
	if output != "[user cancelled execution]" {
		fmt.Printf("%s ✅ %s\n", ui.Styles["TagSystem"].Render("[SYS]"), ui.Styles["Success"].Render("Command succeeded"))
		fmt.Printf("%s\n%s\n", ui.Styles["TagResult"].Render("[RES]"), ui.Styles["Cmd"].Render(output))
	} else {
		fmt.Printf("%s ⏭️ %s\n", ui.Styles["TagSystem"].Render("[SYS]"), ui.Styles["Subdued"].Render("Skipped"))
	}

	return nil
}
