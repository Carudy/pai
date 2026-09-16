// Package prompts holds PAI's role and tool definitions.
//
// A role is pure data: an intro (its system prompt) plus the set of tools it
// may use. Roles are resolved from, in order:
//
//	~/.config/pai/roles/<name>.toml   (user-defined, wins)
//	internal/prompts/roles/<name>.toml (source checkout, dev hot-reload)
//	embedded roles/<name>.toml         (built-in)
//
// so users can add or override roles without a rebuild. Tool *implementations*
// still live in Go (internal/role/tools.go); a role's `tools` list can only
// reference tools that already exist.
package prompts

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Carudy/pai/internal/paths"
)

//go:embed roles/*.toml tools/*.toml
var builtin embed.FS

// builtinDiskRoot is where the definitions live in a source checkout. It is
// tried before the embedded copy so editing a .toml takes effect without a
// rebuild (a development convenience).
var builtinDiskRoot = filepath.Join("internal", "prompts")

// UserRolesDir returns ~/.config/pai/roles, where users can drop their own role
// definitions. It returns "" if the config directory cannot be determined.
func UserRolesDir() string {
	dir := paths.ConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "roles")
}

// RoleNames returns every available role name (built-in and user-defined),
// deduplicated and sorted.
func RoleNames() []string {
	seen := map[string]bool{}

	if entries, err := builtin.ReadDir("roles"); err == nil {
		for _, e := range entries {
			if name, ok := roleFileName(e); ok {
				seen[name] = true
			}
		}
	}
	if dir := UserRolesDir(); dir != "" {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if name, ok := roleFileName(e); ok {
					seen[name] = true
				}
			}
		}
	}

	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ReadRole returns a role definition plus a human-readable description of where
// it came from (for error messages). A user role takes precedence over the
// built-in one of the same name.
func ReadRole(name string) (data []byte, source string, err error) {
	if err := validName(name); err != nil {
		return nil, "", err
	}

	if dir := UserRolesDir(); dir != "" {
		path := filepath.Join(dir, name+".toml")
		switch data, err := os.ReadFile(path); {
		case err == nil:
			return data, path, nil
		case !os.IsNotExist(err):
			return nil, "", fmt.Errorf("read role %q: %w", name, err)
		}
	}

	rel := "roles/" + name + ".toml"
	if data, err := os.ReadFile(filepath.Join(builtinDiskRoot, rel)); err == nil {
		return data, filepath.Join(builtinDiskRoot, rel), nil
	}
	data, err = builtin.ReadFile(rel)
	if err != nil {
		return nil, "", fmt.Errorf("role %q not found (checked %s and the built-ins)", name, UserRolesDir())
	}
	return data, "built-in:" + rel, nil
}

// ReadTool returns a built-in tool definition plus a human-readable description
// of where it came from.
func ReadTool(name string) (data []byte, source string, err error) {
	if err := validName(name); err != nil {
		return nil, "", err
	}

	rel := "tools/" + name + ".toml"
	if data, err := os.ReadFile(filepath.Join(builtinDiskRoot, rel)); err == nil {
		return data, filepath.Join(builtinDiskRoot, rel), nil
	}
	data, err = builtin.ReadFile(rel)
	if err != nil {
		return nil, "", fmt.Errorf("tool %q not found; built-in tools are: %s",
			name, strings.Join(ToolNames(), ", "))
	}
	return data, "built-in:" + rel, nil
}

// ToolNames returns the names of all built-in tools, sorted.
func ToolNames() []string {
	entries, err := builtin.ReadDir("tools")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if name, ok := roleFileName(e); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// roleFileName extracts a definition name from a directory entry, if the entry
// is a .toml file.
func roleFileName(e fs.DirEntry) (string, bool) {
	if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
		return "", false
	}
	return strings.TrimSuffix(e.Name(), ".toml"), true
}

// validName rejects names that could escape the definitions directory.
func validName(name string) error {
	if name == "" {
		return fmt.Errorf("empty role/tool name")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid role/tool name %q", name)
	}
	return nil
}
