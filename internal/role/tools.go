package role

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/tool"
)

// confirmTitle annotates a confirmation with how many commands it covers, so a
// long "aa && bb && cc" is obvious at the moment of approving it.
func confirmTitle(verb, cmd string) string {
	if n := len(tool.SplitSegments(cmd)); n > 1 {
		return fmt.Sprintf("%s (%d commands)", verb, n)
	}
	return verb
}

// toolHandler executes a single tool. It reports progress through the Runtime's
// Observer and returns the observation text to append to the conversation
// history (including its bracketed label), so the model can read the result on
// the next turn.
type toolHandler func(ctx context.Context, cfg *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error)

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

// toolStream routes a tool's streamed output through the observer, so the
// adapter owns how it is displayed.
func toolStream(rt *Runtime) core.WriterFunc {
	return func(p []byte) (int, error) {
		rt.Observer.ToolOutput(string(p))
		return len(p), nil
	}
}

// cancelled is the result of a user declining to run a tool.
func cancelled() tool.ExecResult {
	return tool.ExecResult{ExitCode: -1, Output: tool.CancelledOutput}
}

// report renders a tool outcome to the observer.
func report(rt *Runtime, output tool.ExecResult, execErr error, okMsg string) {
	switch {
	case errors.Is(execErr, context.Canceled):
		rt.Observer.ToolResult(core.ToolResult{Message: "Interrupted"})
	case output.Output == tool.CancelledOutput:
		rt.Observer.ToolResult(core.ToolResult{Skipped: true, Message: "Skipped"})
	case execErr != nil:
		rt.Observer.ToolResult(core.ToolResult{
			Message: fmt.Sprintf("%s: %v", okMsg, execErr),
			Detail:  output.Output,
		})
	default:
		rt.Observer.ToolResult(core.ToolResult{OK: true, Message: okMsg})
	}
}

// runExecute runs a shell command locally.
func runExecute(ctx context.Context, cfg *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error) {
	var cmd string
	if err := json.Unmarshal(payload, &cmd); err != nil {
		return "", fmt.Errorf("execute payload: %w", err)
	}

	trusted := tool.IsTrusted(cmd, cfg.TrustedCmds)
	rt.Observer.ToolCall(core.ToolCall{
		Name:    "execute",
		Target:  tool.Shell(),
		Detail:  cmd,
		Reason:  reason,
		Trusted: trusted,
	})

	if !trusted {
		ok, err := rt.Prompter.Confirm(confirmTitle("Execute this command?", cmd))
		if err != nil {
			return "", fmt.Errorf("user interaction error: %w", err)
		}
		if !ok {
			output := cancelled()
			report(rt, output, nil, "Command succeeded")
			return observation("cmd result", cmd, nil, output, cfg.TruncateExecLimit), nil
		}
	}

	output, execErr := tool.ExecuteCommand(ctx, cmd, toolStream(rt))
	report(rt, output, execErr, "Command succeeded")
	return observation("cmd result", cmd, execErr, output, cfg.TruncateExecLimit), nil
}

// runRemote runs a command on a remote host over SSH.
func runRemote(ctx context.Context, cfg *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error) {
	var rp tool.RemotePayload
	if err := json.Unmarshal(payload, &rp); err != nil {
		return "", fmt.Errorf("remote payload: %w", err)
	}
	if rt.Remote == nil {
		rm, err := tool.NewRemoteManager(cfg.RemoteShell)
		if err != nil {
			return "", fmt.Errorf("init remote sessions: %w", err)
		}
		rt.Remote = rm
	}

	trusted := tool.IsTrusted(rp.Cmd, cfg.TrustedCmds)
	rt.Observer.ToolCall(core.ToolCall{
		Name:    "remote",
		Target:  rp.Host,
		Detail:  rp.Cmd,
		Reason:  reason,
		Trusted: trusted,
	})

	if !trusted {
		ok, err := rt.Prompter.Confirm(confirmTitle(fmt.Sprintf("Run on %s?", rp.Host), rp.Cmd))
		if err != nil {
			return "", fmt.Errorf("user interaction error: %w", err)
		}
		if !ok {
			output := cancelled()
			report(rt, output, nil, "Remote command succeeded")
			return observation("remote result", rp.Cmd, nil, output, cfg.TruncateExecLimit), nil
		}
	}

	output, execErr := rt.Remote.ExecuteRemote(ctx, rp, toolStream(rt))
	report(rt, output, execErr, "Remote command succeeded")
	return observation("remote result", rp.Cmd, execErr, output, cfg.TruncateExecLimit), nil
}

// runWebsearch searches the web for current information. A search failure is
// non-fatal: the error is fed back so the agent can adapt.
func runWebsearch(ctx context.Context, cfg *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error) {
	var query string
	if err := json.Unmarshal(payload, &query); err != nil {
		return "", fmt.Errorf("websearch payload: %w", err)
	}

	rt.Observer.ToolCall(core.ToolCall{
		Name:   "websearch",
		Detail: query,
		Reason: reason,
	})

	sr, err := tool.Search(ctx, query, cfg.TavilyAPIKey)
	if err != nil {
		rt.Observer.ToolResult(core.ToolResult{Message: fmt.Sprintf("Web search failed: %v", err)})
		return fmt.Sprintf("[search error]\nSEARCH QUERY: %s\nERROR: %v", query, err), nil
	}

	// Keep the top few results for agent context (the summary shows the original count).
	total := len(sr.Results)
	if total > 3 {
		sr.Results = sr.Results[:3]
	}

	rt.Observer.ToolResult(core.ToolResult{
		OK:      true,
		Message: fmt.Sprintf("%d results in %.2fs", total, sr.ResponseTime),
		Detail:  searchPreview(sr),
	})

	observation := fmt.Sprintf("SEARCH QUERY: %s\nRESULTS:\n%s",
		query, chat.TruncateOutput(sr.Format(), cfg.TruncateSearchLimit))
	return "[search result]\n" + observation, nil
}

// observation formats the history entry fed back to the model.
func observation(label, cmd string, execErr error, output tool.ExecResult, limit int) string {
	return fmt.Sprintf("[%s]\nCOMMAND: %s\nEXIT_ERROR: %v\nOUTPUT:\n%s",
		label, cmd, execErr, chat.TruncateOutput(output.String(), limit))
}

// searchPreview renders the short, human-facing summary of a search (the AI
// answer plus result titles).
func searchPreview(sr *tool.SearchResult) string {
	var b strings.Builder
	if sr.Answer != "" {
		b.WriteString(sr.Answer)
		b.WriteString("\n")
	}
	for i, r := range sr.Results {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, r.Title)
	}
	return strings.TrimRight(b.String(), "\n")
}
