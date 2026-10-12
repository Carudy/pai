// Package runner hosts transport-independent, concurrent role runtimes.
package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

type Meta struct {
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Model     string    `json:"model"`
	Cwd       string    `json:"cwd"`
	UpdatedAt time.Time `json:"updated_at"`
}
type History struct {
	Meta
	Turns []core.Turn `json:"turns"`
	Total int         `json:"total"`
}

// Prepare transfers ownership of its resources to the worker. Cleanup is called
// exactly once, including when Prepare returns an error with a cleanup function.
// Backend methods must be concurrency-safe; Prepare must return promptly.
type Backend interface {
	List() ([]Meta, error)
	History(name string, offset, limit int) (History, error)
	Prepare(name string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), error)
}

// The interfaces below are OPTIONAL capability ports. The manager type-asserts
// each one, so a host implements only the subset its backend supports; a method
// made mandatory on Backend would break backends that cannot provide it.

// WorkspaceBackend resolves the immutable workspace when preparing a runtime.
// Resource ownership follows Backend.Prepare, including on failure.
type WorkspaceBackend interface {
	PrepareWorkspace(name, cwd string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), string, error)
}

type CreationBackend interface {
	Create(name, cwd string) (Meta, error)
}

// RoleBackend exposes role definitions without coupling transports to prompts.
type RoleBackend interface {
	Roles() ([]string, string, error)
	CreateWithRole(name, cwd, role string) (Meta, error)
}

// ModelBackend is optional; model discovery must not require remote API calls.
type ModelBackend interface {
	Models() ([]string, string, error)
	SetModel(name, model string) error
}

// RewindBackend is optional; truncating a session's persisted history must not
// require a backend that cannot persist. backup requests a snapshot of the
// dropped turns (nil lets the backend choose its configured default).
type RewindBackend interface {
	Rewind(name string, keep int, backup *bool) error
}

// RoleSetter is optional; changing a session's role must not require a backend
// that cannot persist roles.
type RoleSetter interface {
	SetRole(name, role string) error
}

// WorkingDirSetter is optional; changing a workspace requires a backend that
// can persist it independently of the runtime.
type WorkingDirSetter interface {
	SetWorkingDir(name, cwd string) error
}

// RenameBackend is optional; renaming a session must not require a backend that
// cannot persist.
type RenameBackend interface {
	Rename(name, newName string) error
}

// DeleteBackend is optional; deleting a session must not require a backend that
// cannot persist.
type DeleteBackend interface {
	Delete(name string) error
}

type Prompt struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Title     string         `json:"title"`
	Tool      *core.ToolCall `json:"tool,omitempty"`
	Untrusted []string       `json:"untrusted,omitempty"`
	// TrustTarget says what Untrusted names are: "command" for command names or
	// "path" for a directory, so a client can word the approval correctly.
	TrustTarget string `json:"trust_target,omitempty"`
}

func clonePrompt(p *Prompt) *Prompt {
	if p == nil {
		return nil
	}
	copy := *p
	copy.Tool = cloneToolCall(p.Tool)
	copy.Untrusted = append([]string(nil), p.Untrusted...)
	return &copy
}

// cloneToolCall deep-copies a ToolCall's slices, so every subscriber or snapshot
// gets an independent value (mutating one never leaks into another).
func cloneToolCall(c *core.ToolCall) *core.ToolCall {
	if c == nil {
		return nil
	}
	dup := *c
	dup.CommandSegments = append([]string(nil), c.CommandSegments...)
	dup.UntrustedSegments = append([]int(nil), c.UntrustedSegments...)
	return &dup
}

type Snapshot struct {
	Usage      core.Usage `json:"usage"`
	TotalUsage core.Usage `json:"total_usage"`
	UsageCalls int        `json:"usage_calls"`
	// ContextTokens is the configured compaction threshold: the last prompt size
	// at which older turns get summarized. 0 means compaction is disabled, so a
	// client should not render a context budget.
	ContextTokens int            `json:"context_tokens,omitempty"`
	Name          string         `json:"name"`
	Model         string         `json:"model"`
	State         string         `json:"state"` // starting, busy, awaiting, stopped
	Phase         string         `json:"phase"`
	ActiveTool    *core.ToolCall `json:"active_tool,omitempty"`
	Queued        int            `json:"queued"`
	Pending       *Prompt        `json:"pending,omitempty"`
	Error         string         `json:"error,omitempty"`
	Reasoning     string         `json:"reasoning,omitempty"`
	// TrustAll reports that every confirmation for this session is auto-approved
	// without a prompt. It is a per-session, in-memory toggle the user sets
	// explicitly; it is never persisted to config.
	TrustAll bool `json:"trust_all,omitempty"`
}

func cloneSnapshot(s Snapshot) Snapshot {
	s.ActiveTool = cloneToolCall(s.ActiveTool)
	s.Pending = clonePrompt(s.Pending)
	return s
}

// ResolveWorkingDir validates an existing directory without changing process cwd.
func ResolveWorkingDir(cwd string) (string, error) {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("stat working directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("working directory is not a directory")
	}
	return dir, nil
}
