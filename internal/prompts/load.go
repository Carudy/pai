package prompts

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

// Role is one role definition, parsed from roles/<name>.toml. Locating a
// user-defined override or a built-in stays in ReadRole; this is the parsed
// shape consumers work with, so only this package knows the file format.
type Role struct {
	Name        string   `toml:"name"`
	Description string   `toml:"description"`
	Intro       string   `toml:"intro"`
	Tools       []string `toml:"tools"`
	// ContextFiles names project instruction files to fold into the prompt, e.g.
	// ["AGENTS.md", "CLAUDE.md"]. Empty means the role wants none.
	ContextFiles []string `toml:"context_files"`
}

// Tool is one tool definition, parsed from tools/<name>.toml.
type Tool struct {
	Name        string `toml:"name"`
	Brief       string `toml:"brief"`       // one-liner, used in the compact per-turn reminder
	Description string `toml:"description"` // detailed prose, rendered once into the frozen head
	Schema      string `toml:"schema"`      // payload shape, rendered into the frozen head
}

// LoadRole resolves a role (user-defined or built-in) and parses it. The parse
// error names the file it came from, since a broken user override otherwise
// looks like a broken built-in.
func LoadRole(name string) (*Role, error) {
	data, source, err := ReadRole(name)
	if err != nil {
		return nil, err
	}
	var role Role
	if err := toml.Unmarshal(data, &role); err != nil {
		return nil, fmt.Errorf("parse role %q (%s): %w", name, source, err)
	}
	return &role, nil
}

// LoadTool resolves and parses a tool definition.
func LoadTool(name string) (*Tool, error) {
	data, source, err := ReadTool(name)
	if err != nil {
		return nil, err
	}
	var tool Tool
	if err := toml.Unmarshal(data, &tool); err != nil {
		return nil, fmt.Errorf("parse tool %q (%s): %w", name, source, err)
	}
	return &tool, nil
}
