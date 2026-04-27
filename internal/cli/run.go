package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"pai/internal/agent"
	"pai/internal/config"
	"pai/internal/llm"
	"pai/internal/tool"
	"pai/internal/ui"
)

func local_logger(_type string, outio io.Writer) func(format string, a ...any) {
	if _type == "Debug" {
		return func(format string, a ...any) {
			if Flags.Debug {
				msg := fmt.Sprintf(format, a...)
				trimmed := strings.TrimRight(msg, "\n")
				fmt.Fprint(outio, ui.Styles["Debug"].Render(trimmed))
				// Restore trailing newlines that were stripped for clean rendering.
				fmt.Fprint(outio, "\n")
			}
		}
	}

	if _type == "Normal" {
		return func(format string, a ...any) {
			fmt.Fprintf(outio, format, a...)
		}
	}

	return func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		trimmed := strings.TrimRight(msg, "\n")
		fmt.Fprint(outio, ui.Styles[_type].Render(trimmed))
		fmt.Fprint(outio, "\n")
	}
}

func Run(ctx context.Context, stdin io.Reader, stdout io.Writer, args []string) int {
	GetFlags(args)

	DebugLog := local_logger("Debug", stdout)
	ErrorLog := local_logger("Error", stdout)

	DebugLog("📃 User Flags: %v\n", Flags)
	DebugLog("🔧 Loading configuration...\n")

	cfg, err := config.LoadUserConfig()
	if err != nil {
		ErrorLog("Error loading config: %v\n", err)
		return 1
	}
	if Flags.Action != "" {
		cfg.DefaultAgent = Flags.Action
	}

	DebugLog("🔧 User config: %v\n", cfg)
	DebugLog("🔌 Connecting to %s...\n", cfg.DefaultModel)

	llm_client, err := llm.CreateClient(cfg.Provider, cfg.APIKeys[cfg.Provider], cfg.Model, cfg.Reasoning)
	if err != nil {
		ErrorLog("Error creating LLM client: %v\n", err)
		return 1
	}
	cfg.Clients[cfg.DefaultAgent] = llm_client

	user_input := strings.TrimSpace(Flags.Input)
	if user_input == "" && !Flags.Multi {
		ErrorLog("Error: Please provide a user input\n")
		return 1
	}
	DebugLog("💬 User input: %s...\n", user_input)

	switch cfg.DefaultAgent {
	case "qa":
		DebugLog("Entering ask-ans agent; Inter: %v\n", Flags.Multi)
		if err := agent.QA(ctx, cfg, user_input, Flags.Multi); err != nil {
			ErrorLog("Error in asking agent: %v\n", err)
			return 1
		}
		return 0

	case "devops":
		DebugLog("Entering devops agent\n")
		cfg.Clients["cmd"] = cfg.Clients["devops"]
		if err := agent.DevOps(ctx, cfg, user_input); err != nil {
			ErrorLog("Error in devops agent: %v\n", err)
			return 1
		}
		return 0

	case "cmd":
		DebugLog("Entering cmd-gen agent\n")
		result, err := agent.GenCMD(ctx, cfg, user_input)
		if err != nil {
			ErrorLog("Error generating command: %v\n", err)
			return 1
		}

		fmt.Fprintf(stdout, "%s %s\n", ui.Styles["TagExec"].Render("[Exec]"), ui.Styles["Info"].Render(result.Comment))
		fmt.Fprintf(stdout, "%s %s\n", ui.Styles["TagExec"].Render("[Exec]"), ui.Styles["Cmd"].Render(result.Cmd))

		output, execErr := tool.ExecuteCommand(os.Stdout, result.Cmd, true)
		if execErr != nil {
			fmt.Fprintf(stdout, "%s %s\n", ui.Styles["TagSystem"].Render("[Sys]"), ui.Styles["Warn"].Render("Command failed"))
			if output != "" {
				fmt.Fprintf(stdout, "%s\n%s\n", ui.Styles["TagResult"].Render("[Res]"), ui.Styles["Warn"].Render(output))
			}
			return 1
		}
		if output != "[user cancelled execution]" {
			fmt.Fprintf(stdout, "%s %s\n", ui.Styles["TagSystem"].Render("[Sys]"), ui.Styles["Success"].Render("Command succeeded"))
			fmt.Fprintf(stdout, "%s\n%s\n", ui.Styles["TagResult"].Render("[Res]"), ui.Styles["Cmd"].Render(output))
		} else {
			fmt.Fprintf(stdout, "%s %s\n", ui.Styles["TagSystem"].Render("[Sys]"), ui.Styles["Subdued"].Render("Skipped"))
		}

		return 0

	default:
		ErrorLog("Error: Unsupported PAI action: \"%s\"\n", cfg.DefaultAgent)
		return 1
	}
}
