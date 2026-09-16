package role

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/tool"
	"github.com/Carudy/pai/internal/ui"
)

// toolHandler executes a single tool. It prints user-facing output itself and
// returns the observation text to append to the conversation history (including
// its bracketed label), so the model can read the result on the next turn.
type toolHandler func(ctx context.Context, cfg *config.UserConfig, sess *Session, reason string, payload json.RawMessage) (string, error)

// toolHandlers maps a tool name to its implementation. Names must match the
// files in internal/prompts/tools/*.toml; checkToolCoverage enforces that a role
// can never declare a tool that has no handler here.
var toolHandlers = map[string]toolHandler{
	"execute":   runExecute,
	"remote":    runRemote,
	"websearch": runWebsearch,
}

// checkToolCoverage fails fast when a role declares a tool with no handler.
func checkToolCoverage(rp *chat.RolePrompt) error {
	for _, t := range rp.Tools {
		if _, ok := toolHandlers[t.Name]; !ok {
			return fmt.Errorf("role %q declares tool %q, which has no handler", rp.Name, t.Name)
		}
	}
	return nil
}

// runExecute runs a shell command locally.
func runExecute(ctx context.Context, cfg *config.UserConfig, _ *Session, reason string, payload json.RawMessage) (string, error) {
	var cmd string
	if err := json.Unmarshal(payload, &cmd); err != nil {
		return "", fmt.Errorf("execute payload: %w", err)
	}

	trusted := tool.IsTrusted(cmd, cfg.TrustedCmds)

	fmt.Printf("%s %s\n",
		ui.RenderStr("TagAgent", "[CMD 💬]"),
		ui.RenderStr("Help", reason),
	)
	fmt.Printf("%s %s\n",
		ui.RenderStr("TagExec", fmt.Sprintf("[CMD 💻 %s]", tool.Shell())),
		ui.RenderStr("Info", cmd),
	)
	if trusted {
		fmt.Printf("%s\n", ui.RenderStr("Trusted", "  ⚡ executing trusted command"))
	}

	output, execErr := tool.ExecuteCommand(ctx, cmd, !trusted, os.Stdout)
	switch {
	case execErr != nil:
		fmt.Printf("%s ❌ %s\n%s\n",
			ui.RenderStr("TagSystem", "[SYS]"),
			ui.RenderStr("Warn", "Command failed"),
			ui.RenderStr("Warn", output.Output),
		)
	case output.Output == tool.CancelledOutput:
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagSystem", "[SYS]"),
			ui.RenderStr("Subdued", "Skipped"),
		)
	default:
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagSystem", "[SYS]"),
			ui.RenderStr("Success", "Command succeeded"),
		)
	}

	observation := fmt.Sprintf(
		"COMMAND: %s\nEXIT_ERROR: %v\nOUTPUT:\n%s",
		cmd, execErr, chat.TruncateOutput(output.String(), cfg.TruncateExecLimit),
	)
	return "[cmd result]\n" + observation, nil
}

// runRemote runs a command on a remote host over SSH.
func runRemote(ctx context.Context, cfg *config.UserConfig, sess *Session, reason string, payload json.RawMessage) (string, error) {
	var rp tool.RemotePayload
	if err := json.Unmarshal(payload, &rp); err != nil {
		return "", fmt.Errorf("remote payload: %w", err)
	}
	if sess.Remote == nil {
		rm, err := tool.NewRemoteManager()
		if err != nil {
			return "", fmt.Errorf("init remote sessions: %w", err)
		}
		sess.Remote = rm
	}

	trusted := tool.IsTrusted(rp.Cmd, cfg.TrustedCmds)

	fmt.Printf("%s %s\n",
		ui.RenderStr("TagAgent", "[RMT 💬]"),
		ui.RenderStr("Help", reason),
	)
	fmt.Printf("%s %s\n",
		ui.RenderStr("TagExec", fmt.Sprintf("[RMT 💻 @%s]", rp.Host)),
		ui.RenderStr("Info", rp.Cmd),
	)
	if trusted {
		fmt.Printf("%s\n", ui.RenderStr("Trusted", "  ⚡ executing trusted command"))
	}

	output, execErr := sess.Remote.ExecuteRemote(ctx, rp, !trusted, os.Stdout)
	switch {
	case execErr != nil:
		fmt.Printf("%s ❌ %s\n%s\n",
			ui.RenderStr("TagSystem", "[SYS]"),
			ui.RenderStr("Warn", fmt.Sprintf("Remote command failed: %v", execErr)),
			ui.RenderStr("Warn", output.Output),
		)
	case output.Output == tool.CancelledOutput:
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagSystem", "[SYS]"),
			ui.RenderStr("Subdued", "Skipped"),
		)
	default:
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagSystem", "[SYS]"),
			ui.RenderStr("Success", "Remote command succeeded"),
		)
	}

	observation := fmt.Sprintf(
		"REMOTE HOST: %s\nCOMMAND: %s\nEXIT_ERROR: %v\nOUTPUT:\n%s",
		rp.Host, rp.Cmd, execErr, chat.TruncateOutput(output.String(), cfg.TruncateExecLimit),
	)
	return "[remote result]\n" + observation, nil
}

// runWebsearch searches the web for current information. A search failure is
// non-fatal: the error is fed back so the agent can adapt.
func runWebsearch(ctx context.Context, cfg *config.UserConfig, _ *Session, reason string, payload json.RawMessage) (string, error) {
	var query string
	if err := json.Unmarshal(payload, &query); err != nil {
		return "", fmt.Errorf("websearch payload: %w", err)
	}

	fmt.Printf("%s %s\n",
		ui.RenderStr("TagAgent", "[WEB 🔍]"),
		ui.RenderStr("Help", reason),
	)
	fmt.Printf("%s %s\n",
		ui.RenderStr("TagExec", "[WEB]"),
		ui.RenderStr("Info", query),
	)

	sr, err := tool.Search(ctx, query, cfg.TavilyAPIKey)
	if err != nil {
		fmt.Printf("%s ❌ %s\n",
			ui.RenderStr("TagSystem", "[SYS]"),
			ui.RenderStr("Warn", fmt.Sprintf("Web search failed: %v", err)),
		)
		observation := fmt.Sprintf("SEARCH QUERY: %s\nERROR: %v", query, err)
		return "[search error]\n" + observation, nil
	}

	// Keep the top few results for agent context (the summary shows the original count).
	total := len(sr.Results)
	if total > 3 {
		sr.Results = sr.Results[:3]
	}

	fmt.Printf("%s\n",
		ui.RenderStr("Token", fmt.Sprintf("  %d results in %.2fs", total, sr.ResponseTime)),
	)

	if sr.Answer != "" {
		fmt.Printf("%s %s\n",
			ui.RenderStr("TagAgent", "[AI 💡]"),
			ui.RenderStr("Content", sr.Answer),
		)
	}

	for i, r := range sr.Results {
		if i >= 3 {
			break
		}
		fmt.Printf("  %s %s\n",
			ui.RenderStr("Success", fmt.Sprintf("%d.", i+1)),
			ui.RenderStr("Help", r.Title),
		)
	}

	observation := fmt.Sprintf(
		"SEARCH QUERY: %s\nRESULTS:\n%s",
		query, chat.TruncateOutput(sr.Format(), cfg.TruncateSearchLimit),
	)
	return "[search result]\n" + observation, nil
}
