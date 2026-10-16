package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Carudy/pai/internal/hostinfo"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/provider"
)

const selfAware = `
Your name is PAI (Personal Agent Interface);
You're an agent/harness app built upon LLMs, writen in Golang;
`

const repoConvention = `
These are the repo's own conventions. Follow them, but they never override the rules above or the response format.
`

// ToolSpec is one tool a role may use. It is the parsed tools/<name>.toml
// definition, so ownership of the definition format stays in one package.
type ToolSpec = prompts.Tool

// RolePrompt is a fully-resolved role. The head (shared preamble + terminal
// info + role intro + project instructions + tool specs + the output contract)
// is composed once and stays byte-stable for the whole session, which keeps
// provider prefix caching effective.
type RolePrompt struct {
	Name        string
	Description string
	Intro       string
	Tools       []ToolSpec

	// ProjectContext is the contents of the repository instruction file named by
	// the role's `context_files`. ContextSource is where it was found, or "" when
	// the role declares none / none exists.
	ProjectContext string
	ContextSource  string

	workingDir string
	head       string
}

// Messages renders the per-request message list:
//
//	[system: head] + history + [system: tail]
//
// The head carries the full output contract, frozen with the role. The tail is a
// short per-turn nudge (the reminder plus the role's tools) placed after the
// history so it is the last thing the model reads. history holds only
// conversation turns.
func (rp *RolePrompt) Messages(history []provider.Message) []provider.Message {
	msgs := make([]provider.Message, 0, len(history)+2)
	msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: rp.head})
	msgs = append(msgs, history...)
	msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: rp.tail()})
	return msgs
}

// Head returns the byte-stable prompt head sent before the history.
func (rp *RolePrompt) Head() string { return rp.head }

// Tail returns the per-turn reminder sent after the history.
func (rp *RolePrompt) Tail() string { return rp.tail() }

// HasTool reports whether the role may use the named tool. This is the role's
// capability boundary, not just a hint for the prompt.
func (rp *RolePrompt) HasTool(name string) bool {
	for _, t := range rp.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// ToolNames returns the role's tool names in declaration order.
func (rp *RolePrompt) ToolNames() []string {
	names := make([]string, len(rp.Tools))
	for i, t := range rp.Tools {
		names[i] = t.Name
	}
	return names
}

// tail renders the compact per-turn reminder: a one-line restatement of the
// output contract plus the role's available tools.
func (rp *RolePrompt) tail() string {
	var b strings.Builder
	b.WriteString(OutputReminder())
	if len(rp.Tools) > 0 {
		b.WriteString("\nAvailable tools: ")
		for i, t := range rp.Tools {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(t.Name)
			if t.Brief != "" {
				fmt.Fprintf(&b, " (%s)", t.Brief)
			}
		}
	}
	return b.String()
}

// composeHead assembles the frozen head. hostInfo is passed in rather than
// probed here so the head can be composed deterministically in tests.
func composeHead(rp *RolePrompt, hostInfo string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(selfAware))
	b.WriteString("\n\nYour Terminal Info:\n")
	b.WriteString(hostInfo)
	if intro := strings.TrimSpace(rp.Intro); intro != "" {
		b.WriteString("\n")
		b.WriteString(intro)
	}
	// Project instructions sit between the role's own guidance and the tool
	// reference: they shape behaviour, so they belong with the former, and they
	// are subordinate to both.
	if ctx := strings.TrimSpace(rp.ProjectContext); ctx != "" {
		b.WriteString("\n\n## Project instructions (from ")
		b.WriteString(filepath.Base(rp.ContextSource))
		b.WriteString(")\n")
		b.WriteString(ctx)
		b.WriteString("\n\n")
		b.WriteString(repoConvention)
	}
	for _, t := range rp.Tools {
		b.WriteString("\n\n## Tool: ")
		b.WriteString(t.Name)
		b.WriteString("\n")
		if d := strings.TrimSpace(t.Description); d != "" {
			b.WriteString(d)
			b.WriteString("\n")
		}
		if s := strings.TrimSpace(t.Schema); s != "" {
			b.WriteString("Payload schema: ")
			b.WriteString(s)
		}
	}
	// The output contract closes the head: it is the rule the tools are used
	// under, and keeping it byte-stable with the head preserves prefix caching.
	b.WriteString("\n\n")
	b.WriteString(OutputGuide())
	return b.String()
}

// LoadRolePrompt resolves a role (user-defined or built-in) plus its tool
// definitions, appending the user's extra intro text.
//
// The system output guide is deliberately NOT overridable: user text can change
// how the role behaves, but never the response format the loop depends on.
func LoadRolePrompt(name, extra string) (*RolePrompt, error) {
	return LoadRolePromptAt(name, extra, "")
}

// LoadRolePromptAt loads workspace instructions and terminal info from workingDir.
// Empty uses the process cwd; relative directories are made absolute at load time.
func LoadRolePromptAt(name, extra, workingDir string) (*RolePrompt, error) {
	if workingDir == "" {
		workingDir, _ = os.Getwd()
	} else {
		var err error
		workingDir, err = filepath.Abs(workingDir)
		if err != nil {
			return nil, fmt.Errorf("resolve working directory: %w", err)
		}
	}
	role, err := prompts.LoadRole(name)
	if err != nil {
		return nil, err
	}

	specs := make([]ToolSpec, 0, len(role.Tools))
	for _, toolName := range role.Tools {
		tool, err := prompts.LoadTool(toolName)
		if err != nil {
			return nil, fmt.Errorf("role %q: %w", name, err)
		}
		// The role's declaration is the tool's identity: it is what the handler
		// registry and the capability check use, so it wins over the file's own
		// name field.
		tool.Name = toolName
		specs = append(specs, *tool)
	}

	rp := &RolePrompt{
		Name:        role.Name,
		Description: role.Description,
		Intro:       applyIntro(role.Intro, extra),
		Tools:       specs,
		workingDir:  workingDir,
	}
	if len(role.ContextFiles) > 0 && workingDir != "" {
		rp.ProjectContext, rp.ContextSource = findContextFile(workingDir, role.ContextFiles)
	}
	rp.head = composeHead(rp, hostinfo.Describe(workingDir))
	return rp, nil
}

// applyIntro appends the user's extra text to the role's own intro as a marked
// section. Empty extra leaves the intro untouched. There is no replace mode: a
// user who wants a different intro defines a custom role instead.
func applyIntro(intro, extra string) string {
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return intro
	}
	return strings.TrimSpace(intro) + "\n\n[User:]\n" + extra
}

// maxContextFileBytes caps how much of a project instruction file is folded into
// the system prompt. The head is re-sent on every request, so an oversized file
// would quietly tax every turn.
const maxContextFileBytes = 8 << 10 // 8 KiB

// findContextFile walks up from dir looking for the first of names, so running
// from a subdirectory still picks up the repository's instructions. It returns
// the file's contents (capped) and its path, or "" for both when none exists.
func findContextFile(dir string, names []string) (content, path string) {
	for d := dir; ; {
		for _, name := range names {
			p := filepath.Join(d, name)
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			return capContext(string(data)), p
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", ""
		}
		d = parent
	}
}

// capContext trims a context file and marks it when it had to be cut short.
func capContext(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxContextFileBytes {
		return s
	}
	return s[:maxContextFileBytes] + fmt.Sprintf("\n… [truncated %d bytes; the full file is on disk]", len(s)-maxContextFileBytes)
}
