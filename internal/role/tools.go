package role

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// trustedList merges the run's session-trusted names with the config list,
// without mutating cfg.TrustedCmds.
func (rt *Runtime) trustedList(cfg *config.UserConfig) []string {
	if len(rt.TrustedCmds) == 0 {
		return cfg.TrustedCmds
	}
	return append(append([]string{}, cfg.TrustedCmds...), rt.TrustedCmds...)
}

// trustedPaths merges the run's session-trusted directories with the config list.
func (rt *Runtime) trustedPaths(cfg *config.UserConfig) []string {
	if len(rt.TrustedPaths) == 0 {
		return cfg.TrustedPaths
	}
	return append(append([]string{}, cfg.TrustedPaths...), rt.TrustedPaths...)
}

// segmentTexts returns a chained command's segments verbatim, for display by a UI
// that cannot run tool.SplitSegments itself.
func segmentTexts(cmd string) []string {
	segs := tool.SplitSegments(cmd)
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = s.Src
	}
	return out
}

// confirmCommand asks about an untrusted command, offering the trust choices when
// the prompter supports them and a plain yes/no otherwise. With no trustable names
// — a loop, a group, a heredoc to an interpreter — there is nothing to scope a
// "trust from now on" to, so it asks a plain yes/no rather than offering a grant
// that could not be limited.
func confirmCommand(rt *Runtime, title string, untrusted []string) (core.TrustChoice, error) {
	if len(untrusted) > 0 {
		if cc, ok := rt.Prompter.(core.CommandConfirmer); ok {
			return cc.ConfirmCommand(title, untrusted)
		}
	}
	ok, err := rt.Prompter.Confirm(title)
	if err != nil {
		return core.TrustDeny, err
	}
	if ok {
		return core.TrustOnce, nil
	}
	return core.TrustDeny, nil
}

// unsafeTrustNames are interpreters and wrappers: trusting one of them would let
// anything run, so "trust from now on" is downgraded to session scope for them.
var unsafeTrustNames = map[string]bool{
	"sudo": true, "doas": true, "su": true, "env": true, "xargs": true,
	"sh": true, "bash": true, "zsh": true, "fish": true, "eval": true,
	"exec": true, "nohup": true, "nice": true, "timeout": true, "stdbuf": true, "command": true,
}

// confirmPath asks about a file change outside the trusted paths, offering the
// trust choices when the prompter supports them and a plain yes/no otherwise.
func confirmPath(rt *Runtime, title, dir string) (core.TrustChoice, error) {
	if pc, ok := rt.Prompter.(core.PathConfirmer); ok {
		return pc.ConfirmPath(title, dir)
	}
	ok, err := rt.Prompter.Confirm(title)
	if err != nil {
		return core.TrustDeny, err
	}
	if ok {
		return core.TrustOnce, nil
	}
	return core.TrustDeny, nil
}

// applyPathTrust records a directory trust choice. Trusting the filesystem root
// for every future run is refused (session only), and the outcome is reported.
func applyPathTrust(rt *Runtime, choice core.TrustChoice, dir string) {
	if (choice != core.TrustSession && choice != core.TrustPersist) || dir == "" {
		return
	}
	if choice == core.TrustPersist {
		if dir == string(filepath.Separator) {
			rt.Observer.Notice("refusing to trust the filesystem root for every session; trusted for this one")
		} else if err := config.AddTrustedPath(dir); err != nil {
			rt.Observer.Notice("could not save trusted path: " + err.Error())
		} else {
			rt.Observer.Notice("trusted from now on: " + dir)
		}
	}
	rt.TrustedPaths = append(rt.TrustedPaths, dir)
}

// applyTrust records a trust choice for the flagged command names. Persisting is
// downgraded to session scope when a name is an interpreter, and every outcome
// is reported, so "always" is never silently overbroad.
func applyTrust(rt *Runtime, choice core.TrustChoice, names []string) {
	if (choice != core.TrustSession && choice != core.TrustPersist) || len(names) == 0 {
		return
	}
	if choice == core.TrustPersist {
		var unsafe []string
		for _, n := range names {
			if unsafeTrustNames[n] {
				unsafe = append(unsafe, n)
			}
		}
		switch {
		case len(unsafe) > 0:
			rt.Observer.Notice(fmt.Sprintf("%s can run anything; trusted for this session only", strings.Join(unsafe, ", ")))
		default:
			if err := config.AddTrustedCmds(names...); err != nil {
				rt.Observer.Notice("could not save trusted commands: " + err.Error())
			} else {
				rt.Observer.Notice("trusted from now on: " + strings.Join(names, ", "))
			}
		}
	}
	rt.TrustedCmds = append(rt.TrustedCmds, names...)
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
	"execute":     runExecute,
	"remote":      runRemote,
	"websearch":   runWebsearch,
	"papersearch": runPapersearch,
	"read":        runRead,
	"edit":        runEdit,
	"write":       runWrite,
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
	case output.TimedOut:
		// A timeout has no error, so it would otherwise read as a success.
		rt.Observer.ToolResult(core.ToolResult{Message: "Command timed out", Detail: output.Output})
	case execErr != nil:
		rt.Observer.ToolResult(core.ToolResult{
			Message: fmt.Sprintf("%s: %v", okMsg, execErr),
			Detail:  output.Output,
		})
	default:
		rt.Observer.ToolResult(core.ToolResult{OK: true, Message: okMsg})
	}
}

// execPayload is the object form of the execute tool's payload. The plain string
// form (a bare command) is still accepted and means "no explicit directory".
type execPayload struct {
	Cmd string `json:"cmd"`
	Dir string `json:"dir"`
}

// parseExecPayload accepts either a JSON string (the command) or the
// {"cmd":...,"dir":...} object, so the common case stays a bare command.
func parseExecPayload(payload json.RawMessage) (execPayload, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var cmd string
		if err := json.Unmarshal(trimmed, &cmd); err != nil {
			return execPayload{}, fmt.Errorf("execute payload: %w", err)
		}
		return execPayload{Cmd: cmd}, nil
	}
	var p execPayload
	if err := json.Unmarshal(trimmed, &p); err != nil {
		return execPayload{}, fmt.Errorf("execute payload: %w", err)
	}
	return p, nil
}

// resolveExecDir resolves the optional directory an execute command runs in. Empty
// means the run's working directory; a relative dir is joined onto it (or the
// process cwd when there is none), so the model can name a subdirectory without
// knowing its absolute path. The result must be an existing directory, so a bad
// dir is reported before the command runs.
func resolveExecDir(workingDir, dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return workingDir, nil
	}
	if !filepath.IsAbs(dir) {
		if workingDir != "" {
			dir = filepath.Join(workingDir, dir)
		} else if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("working directory %q: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working directory %q is not a directory", dir)
	}
	return dir, nil
}

// runExecute runs a shell command locally.
func runExecute(ctx context.Context, cfg *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error) {
	p, err := parseExecPayload(payload)
	if err != nil {
		return "", err
	}
	cmd := p.Cmd
	dir, err := resolveExecDir(rt.WorkingDir, p.Dir)
	if err != nil {
		return "", err
	}

	trusted := rt.trustedList(cfg)
	isTrusted := tool.IsTrusted(cmd, trusted)
	call := core.ToolCall{
		Name:              "execute",
		Target:            tool.Shell(),
		Detail:            cmd,
		Reason:            reason,
		Trusted:           isTrusted,
		CommandSegments:   segmentTexts(cmd),
		UntrustedSegments: tool.UntrustedSegments(cmd, trusted),
	}
	// Surface only a directory the model asked for; the implicit default is not
	// worth a line in every confirmation.
	if strings.TrimSpace(p.Dir) != "" {
		call.Dir = dir
	}
	rt.Observer.ToolCall(call)

	if !isTrusted {
		names := tool.UntrustedNames(cmd, trusted)
		choice, err := confirmCommand(rt, confirmTitle("Execute this command?", cmd), names)
		if err != nil {
			return "", fmt.Errorf("user interaction error: %w", err)
		}
		if choice == core.TrustDeny {
			output := cancelled()
			report(rt, output, nil, "Command succeeded")
			return observation("execute", cmd, nil, output, execTruncate(cfg)), nil
		}
		applyTrust(rt, choice, names)
	}

	// Bound one command when configured. Zero (the default) leaves it unbounded;
	// the user's Ctrl+C stops a running command instead.
	if d := cfg.CmdTimeout; d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	output, execErr := tool.ExecuteCommandAt(ctx, cmd, dir, toolStream(rt))
	report(rt, output, execErr, "Command succeeded")
	return observation("execute", cmd, execErr, output, execTruncate(cfg)), nil
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

	trusted := rt.trustedList(cfg)
	isTrusted := tool.IsTrusted(rp.Cmd, trusted)
	rt.Observer.ToolCall(core.ToolCall{
		Name:              "remote",
		Target:            rp.Host,
		Detail:            rp.Cmd,
		Reason:            reason,
		Trusted:           isTrusted,
		CommandSegments:   segmentTexts(rp.Cmd),
		UntrustedSegments: tool.UntrustedSegments(rp.Cmd, trusted),
	})

	if !isTrusted {
		names := tool.UntrustedNames(rp.Cmd, trusted)
		choice, err := confirmCommand(rt, confirmTitle(fmt.Sprintf("Run on %s?", rp.Host), rp.Cmd), names)
		if err != nil {
			return "", fmt.Errorf("user interaction error: %w", err)
		}
		if choice == core.TrustDeny {
			output := cancelled()
			report(rt, output, nil, "Remote command succeeded")
			return observation("remote", rp.Cmd, nil, output, execTruncate(cfg)), nil
		}
		applyTrust(rt, choice, names)
	}

	// Same budget as a local command; applied after confirmation so the user's
	// deliberation is not counted against the command's time.
	if d := cfg.CmdTimeout; d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	output, execErr := rt.Remote.ExecuteRemote(ctx, rp, toolStream(rt))
	report(rt, output, execErr, "Remote command succeeded")
	return observation("remote", rp.Cmd, execErr, output, execTruncate(cfg)), nil
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
		query, searchTruncate(cfg).Apply(sr.Format()))
	return "[search result]\n" + observation, nil
}

// Paper metadata stays in complete records rather than using command-style
// head/tail truncation, which can separate an abstract from its identifier.
func runPapersearch(ctx context.Context, cfg *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error) {
	return runPapersearchWith(ctx, cfg, rt, reason, payload, tool.PaperSearch)
}

func runPapersearchWith(ctx context.Context, cfg *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage,
	search func(context.Context, tool.PaperSearchRequest, string) (*tool.PaperSearchResult, error)) (string, error) {
	var req tool.PaperSearchRequest
	err := json.Unmarshal(payload, &req)
	if err == nil {
		err = req.Normalize(cfg.PaperSearchSource)
	}
	rt.Observer.ToolCall(core.ToolCall{Name: "papersearch", Target: req.Source, Detail: req.Query, Reason: reason})
	if err != nil {
		rt.Observer.ToolResult(core.ToolResult{Message: fmt.Sprintf("Paper search failed: %v", err)})
		return fmt.Sprintf("[tool error]\nTOOL: papersearch\nERROR: %v", err), nil
	}

	sr, err := search(ctx, req, cfg.PaperSearchEmail)
	if err != nil {
		rt.Observer.ToolResult(core.ToolResult{Message: fmt.Sprintf("Paper search failed: %v", err)})
		return fmt.Sprintf("[papersearch error]\nSOURCE: %s\nQUERY: %s\nERROR: %v", req.Source, req.Query, err), nil
	}
	var preview strings.Builder
	for i, p := range sr.Papers {
		fmt.Fprintf(&preview, "%d. %s\n", i+1, p.Title)
	}
	// A fallback means the strict query matched nothing; say so in the user's
	// transcript rather than only inside the model's observation.
	if sr.Fallback {
		rt.Observer.Notice(fmt.Sprintf("papersearch %s: the strict query matched nothing; searched any-of keywords instead (%d candidates)", sr.Source, sr.Candidates))
	}
	message := fmt.Sprintf("%d papers from %s", len(sr.Papers), sr.Source)
	if sr.Candidates > len(sr.Papers) {
		message = fmt.Sprintf("%d of %d papers from %s", len(sr.Papers), sr.Candidates, sr.Source)
	}
	rt.Observer.ToolResult(core.ToolResult{
		OK: true, Message: message,
		Detail: strings.TrimSpace(preview.String()),
	})
	return "[papersearch result]\n" + sr.Format(cfg.Context.PaperLimit), nil
}

// observation formats the history entry fed back to the model. The tool name and the
// command share the header line, and the command stays on purpose: once an observation
// is elided, its first lines are all the model sees, so the head has to say what ran and
// not only what came back. The exit status leads the output (tool.ExecResult.String), so
// the old "EXIT_ERROR:" and "OUTPUT:" scaffolding is gone; a non-nil error is the only
// extra line. Naming the tool matches the action's "toolname", so the pair reads as one
// step.
func observation(name, cmd string, execErr error, output tool.ExecResult, trunc chat.Truncate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[tool %s result]", name)
	if cmd = strings.TrimSpace(cmd); cmd != "" {
		b.WriteString(" ")
		b.WriteString(cmd)
	}
	b.WriteString("\n")
	if execErr != nil {
		fmt.Fprintf(&b, "error: %v\n", execErr)
	}
	b.WriteString(trunc.Apply(output.String()))
	return b.String()
}

// execTruncate builds the observation budget for local/remote command output.
func execTruncate(cfg *config.UserConfig) chat.Truncate {
	return chat.Truncate{
		MaxBytes:  cfg.Context.ExecLimit,
		HeadLines: cfg.Context.HeadLines,
		TailLines: cfg.Context.TailLines,
	}
}

// searchTruncate builds the observation budget for web-search results.
func searchTruncate(cfg *config.UserConfig) chat.Truncate {
	return chat.Truncate{
		MaxBytes:  cfg.Context.SearchLimit,
		HeadLines: cfg.Context.HeadLines,
		TailLines: cfg.Context.TailLines,
	}
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
