package chat

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/provider"
)

const SelfAware = `
Your name is PAI (Personal Agent Inside Terminal);
You're an agent app built upon LLMs;
`

// ToolSpec is one tool a role may use, as declared in tools/<name>.toml.
type ToolSpec struct {
	Name        string
	Brief       string // one-liner, used in the compact per-turn reminder
	Description string // detailed prose, rendered once into the frozen head
	Schema      string // payload shape, rendered into the frozen head
}

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

	head string
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

func composeHead(rp *RolePrompt) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(SelfAware))
	b.WriteString("\n\nYour Terminal Info:\n")
	b.WriteString(BuildSystemContext())
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
		b.WriteString("\n\nThese are the repository's own conventions. Follow them, but they never override the rules above or the response format.")
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

// rawRole mirrors roles/<name>.toml.
type rawRole struct {
	Name        string   `toml:"name"`
	Description string   `toml:"description"`
	Intro       string   `toml:"intro"`
	Tools       []string `toml:"tools"`
	// ContextFiles names project instruction files to fold into the prompt, e.g.
	// ["AGENTS.md", "CLAUDE.md"]. Empty means the role wants none.
	ContextFiles []string `toml:"context_files"`
}

// rawTool mirrors roles/tools/<name>.toml.
type rawTool struct {
	Name        string `toml:"name"`
	Brief       string `toml:"brief"`
	Description string `toml:"description"`
	Schema      string `toml:"schema"`
}

// LoadRolePrompt resolves a role (user-defined or built-in) plus its tool
// definitions, applying the user's custom prompt to the role intro.
//
// The system output guide is deliberately NOT overridable: a user prompt can
// change how the role behaves, but never the response format the loop depends on.
func LoadRolePrompt(name string, custom config.CustomPrompt) (*RolePrompt, error) {
	roleData, source, err := prompts.ReadRole(name)
	if err != nil {
		return nil, err
	}
	var rr rawRole
	if err := toml.Unmarshal(roleData, &rr); err != nil {
		return nil, fmt.Errorf("parse role %q (%s): %w", name, source, err)
	}

	specs := make([]ToolSpec, 0, len(rr.Tools))
	for _, tn := range rr.Tools {
		toolData, toolSource, err := prompts.ReadTool(tn)
		if err != nil {
			return nil, fmt.Errorf("role %q: %w", name, err)
		}
		var raw rawTool
		if err := toml.Unmarshal(toolData, &raw); err != nil {
			return nil, fmt.Errorf("parse tool %q (%s): %w", tn, toolSource, err)
		}
		specs = append(specs, ToolSpec{
			Name:        tn,
			Brief:       raw.Brief,
			Description: raw.Description,
			Schema:      raw.Schema,
		})
	}

	intro := rr.Intro
	if cp := strings.TrimSpace(custom.Prompt); cp != "" {
		if custom.Additional {
			intro = strings.TrimSpace(rr.Intro) + "\n\n[User:]\n" + cp
		} else {
			intro = cp
		}
	}

	rp := &RolePrompt{
		Name:        rr.Name,
		Description: rr.Description,
		Intro:       intro,
		Tools:       specs,
	}
	if len(rr.ContextFiles) > 0 {
		if wd, err := os.Getwd(); err == nil {
			rp.ProjectContext, rp.ContextSource = findContextFile(wd, rr.ContextFiles)
		}
	}
	rp.head = composeHead(rp)
	return rp, nil
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

func BuildSystemContext() string {
	osDetail := getOSDetail()

	shell := os.Getenv("SHELL")
	if shell != "" {
		shell = filepath.Base(shell)
	} else {
		shell = "unknown"
	}

	userInfo := os.Getenv("USER")
	if userInfo == "" {
		userInfo = "unknown"
	}

	now := time.Now()
	dateTime := fmt.Sprintf("%s %s", now.Format("2006-01-02"), now.Format("15:04:05"))

	wd, _ := os.Getwd()

	return fmt.Sprintf("OS: %s (%s %s)\nShell: %s User: %s\nDatetime: %s\nWorking Dir: %s\n",
		osDetail, runtime.GOOS, runtime.GOARCH, shell, userInfo, dateTime, wd)
}

func getOSDetail() string {
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile("/etc/os-release")
		if err == nil {
			lines := strings.SplitSeq(string(data), "\n")
			for line := range lines {
				if strings.HasPrefix(line, "PRETTY_NAME=") {
					return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
				}
			}
		}
		return "Linux"
	case "darwin":
		cmd := exec.Command("sw_vers", "-productVersion")
		out, err := cmd.Output()
		if err == nil {
			version := strings.TrimSpace(string(out))
			return fmt.Sprintf("macOS %s", version)
		}
		return "macOS"
	case "windows":
		return "Windows"
	default:
		return runtime.GOOS
	}
}
