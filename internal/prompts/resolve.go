package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"github.com/Carudy/pai/internal/paths"
)

// This file is the capability resolver described by plan/capability_contract.md.
// It turns a role's tool names plus the user tool directory into one
// EffectiveSet, and carries provenance (layer, source, definition hash) that the
// runtime uses for trust and re-approval. Built-in tools keep their Go handler;
// custom tools carry an execution kind.

// ToolKind selects the executor. Built-ins keep their Go handler and ignore it.
type ToolKind string

const (
	ToolBuiltin ToolKind = "builtin"
	ToolCLI     ToolKind = "cli"
	ToolHTTP    ToolKind = "http"
	ToolMCP     ToolKind = "mcp" // reserved; no resolver yet
)

// ToolLayer records where a definition came from.
type ToolLayer string

const (
	LayerBuiltin ToolLayer = "builtin"
	LayerUser    ToolLayer = "user"
	LayerProject ToolLayer = "project"
	LayerMCP     ToolLayer = "mcp" // reserved
)

// ParamType is the type of one custom-tool parameter.
type ParamType string

const (
	ParamString  ParamType = "string"
	ParamInteger ParamType = "integer"
	ParamNumber  ParamType = "number"
	ParamBoolean ParamType = "boolean"
	ParamEnum    ParamType = "enum"
)

// Param describes one typed parameter. The payload must be a JSON object; these
// fields are validated before approval, so the rendered action is exactly what
// runs.
type Param struct {
	Name        string    `toml:"-"`
	Type        ParamType `toml:"type"`
	Required    bool      `toml:"required"`
	Default     any       `toml:"default"`
	Min         *float64  `toml:"min"`
	Max         *float64  `toml:"max"`
	MaxLength   int       `toml:"max_length"`
	Pattern     string    `toml:"pattern"`
	Values      []string  `toml:"values"`
	Description string    `toml:"description"`
}

// CLISpec is the argv executor contract. It never goes through a shell.
type CLISpec struct {
	Command        string            `toml:"command"`
	Args           []string          `toml:"args"`
	Dir            string            `toml:"dir"`
	TimeoutSeconds int               `toml:"timeout_seconds"`
	Env            map[string]string `toml:"env"`
}

// HTTPSpec is the bounded HTTP executor contract.
type HTTPSpec struct {
	Method     string            `toml:"method"`
	URL        string            `toml:"url"`
	Headers    map[string]string `toml:"headers"`
	Auth       string            `toml:"auth"`
	BodyParams []string          `toml:"body_params"`
	BodyExtra  map[string]string `toml:"body_extra"`
	MaxBytes   int               `toml:"max_bytes"`
	Format     string            `toml:"format"`
}

// Grant is the approval unit for a tool. A custom tool is trusted by name; a
// built-in command by command name; a file tool by path.
type Grant struct {
	Unit       string `json:"unit"` // tool | command | path | none
	Key        string `json:"key,omitempty"`
	Persistent bool   `json:"persistent"`
	TrustAll   bool   `json:"trust_all"`
}

// ResolvedTool is one callable tool with provenance and authorization metadata.
type ResolvedTool struct {
	Name        string
	Brief       string
	Description string
	Schema      string // explicit override; otherwise generated from Params

	Kind           ToolKind
	Layer          ToolLayer
	Source         string // human-readable origin, for display and audit
	DefinitionHash string // canonical SHA-256 of the parsed definition

	Params      []Param
	CLI         *CLISpec
	HTTP        *HTTPSpec
	OutputLimit int

	Grant Grant
}

// EffectiveSet is the resolved tool set one run/session may call. Tools preserves
// prompt order (role declaration, then role-extra, then project); ByName makes
// lookups unambiguous.
type EffectiveSet struct {
	Tools  []ResolvedTool
	ByName map[string]ResolvedTool
}

// Names returns the names in prompt order.
func (s EffectiveSet) Names() []string {
	names := make([]string, len(s.Tools))
	for i, t := range s.Tools {
		names[i] = t.Name
	}
	return names
}

// Get returns a resolved tool by name.
func (s EffectiveSet) Get(name string) (ResolvedTool, bool) {
	t, ok := s.ByName[name]
	return t, ok
}

// Has reports whether the set contains name.
func (s EffectiveSet) Has(name string) bool {
	_, ok := s.ByName[name]
	return ok
}

// ResolveOptions describes one resolution. The caller supplies role data and
// paths; this package never reads config itself.
type ResolveOptions struct {
	RoleTools       []string
	RoleExtraTools  []string
	UserToolsDir    string // empty = UserToolsDir()
	Workspace       string // absolute workspace; project tools are scoped to it
	TrustedProjects []string
}

var (
	toolNameRe    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	placeholderRe = regexp.MustCompile(`\{([^}]*)\}`)

	maxBriefChars       = 120
	maxDescriptionBytes = 4 << 10
	maxOutputLimit      = 1 << 20
	maxHTTPBytes        = 4 << 20
)

// ResolveTools builds the effective set for one run or session. Resolution is
// deterministic and side-effect free: it reads definitions but executes nothing.
func ResolveTools(opts ResolveOptions) (EffectiveSet, error) {
	userDir := opts.UserToolsDir
	if userDir == "" {
		userDir = UserToolsDir()
	}
	user, err := loadUserTools(userDir)
	if err != nil {
		return EffectiveSet{}, err
	}
	project, err := loadProjectTools(opts.Workspace, opts.TrustedProjects)
	if err != nil {
		return EffectiveSet{}, err
	}
	for name := range project {
		if _, ok := user[name]; ok {
			return EffectiveSet{}, fmt.Errorf("project tool %q collides with a user tool", name)
		}
	}

	defs := make(map[string]ResolvedTool, len(user)+len(project))
	for name, t := range user {
		defs[name] = t
	}
	for name, t := range project {
		defs[name] = t
	}

	names := make([]string, 0, len(opts.RoleTools)+len(opts.RoleExtraTools))
	names = append(names, opts.RoleTools...)
	names = append(names, opts.RoleExtraTools...)

	set := EffectiveSet{ByName: make(map[string]ResolvedTool, len(names)+len(project))}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return EffectiveSet{}, errors.New("empty tool name in role or role-extra")
		}
		if set.Has(name) {
			continue
		}
		t, err := resolveOne(name, defs)
		if err != nil {
			return EffectiveSet{}, fmt.Errorf("resolve tool %q: %w", name, err)
		}
		set.Tools = append(set.Tools, t)
		set.ByName[name] = t
	}

	// A trusted project's tools are appended to every role in that workspace. The
	// trust gate, not a role edit, is the boundary; order stays deterministic.
	projectNames := make([]string, 0, len(project))
	for name := range project {
		projectNames = append(projectNames, name)
	}
	sort.Strings(projectNames)
	for _, name := range projectNames {
		if set.Has(name) {
			continue
		}
		t := project[name]
		set.Tools = append(set.Tools, t)
		set.ByName[name] = t
	}
	return set, nil
}

// UserToolNames returns the names of TOML files in the user tools directory,
// sorted. Invalid files are included by name; resolution reports the error.
func UserToolNames() []string {
	dir := UserToolsDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".toml"))
	}
	sort.Strings(names)
	return names
}

// ResolveTool resolves one name through the same path a role uses, without
// project trust. It is for inspection and dry runs.
func ResolveTool(name string) (ResolvedTool, error) {
	return ResolveToolIn(name, "", nil)
}

// ResolveToolIn resolves one name with the project trust gate applied to
// workspace. A trusted project's tools are appended, so a project tool can be
// inspected even when no role names it yet.
func ResolveToolIn(name, workspace string, trustedProjects []string) (ResolvedTool, error) {
	set, err := ResolveTools(ResolveOptions{
		RoleTools:       []string{name},
		Workspace:       workspace,
		TrustedProjects: trustedProjects,
	})
	if err != nil {
		return ResolvedTool{}, err
	}
	t, ok := set.Get(name)
	if !ok {
		return ResolvedTool{}, fmt.Errorf("tool %q is not defined", name)
	}
	return t, nil
}

// ProjectToolNames returns the project tool names visible for workspace, or nil
// when the workspace is not under a trusted root.
func ProjectToolNames(workspace string, trustedProjects []string) ([]string, error) {
	tools, err := loadProjectTools(workspace, trustedProjects)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// resolveOne prefers a built-in definition, then a user/project definition.
// User and project tools with a built-in name are rejected while loading, so
// this order cannot shadow one.
func resolveOne(name string, defs map[string]ResolvedTool) (ResolvedTool, error) {
	if isBuiltinTool(name) {
		return resolveBuiltin(name)
	}
	if t, ok := defs[name]; ok {
		return t, nil
	}
	available := ToolNames()
	for n := range defs {
		available = append(available, n)
	}
	sort.Strings(available)
	return ResolvedTool{}, fmt.Errorf("tool is not defined; available: %s", strings.Join(available, ", "))
}

func isBuiltinTool(name string) bool {
	for _, n := range ToolNames() {
		if n == name {
			return true
		}
	}
	return false
}

func resolveBuiltin(name string) (ResolvedTool, error) {
	data, source, err := ReadTool(name)
	if err != nil {
		return ResolvedTool{}, err
	}
	var t Tool
	if err := toml.Unmarshal(data, &t); err != nil {
		return ResolvedTool{}, fmt.Errorf("parse tool %q (%s): %w", name, source, err)
	}
	t.Name = name // the role's declaration is the identity
	rt := ResolvedTool{
		Name:        t.Name,
		Brief:       t.Brief,
		Description: t.Description,
		Schema:      t.Schema,
		Kind:        ToolBuiltin,
		Layer:       LayerBuiltin,
		Source:      source,
		Grant:       builtinGrant(name),
	}
	rt.DefinitionHash, err = hashTool(rt)
	if err != nil {
		return ResolvedTool{}, fmt.Errorf("hash tool %q: %w", name, err)
	}
	return rt, nil
}

// builtinGrant maps the existing approval semantics onto the descriptor. The
// handler still computes the per-command or per-path key.
func builtinGrant(name string) Grant {
	switch name {
	case "execute", "remote":
		return Grant{Unit: "command", Persistent: true, TrustAll: true}
	case "read", "edit", "write":
		return Grant{Unit: "path", Persistent: true, TrustAll: true}
	default:
		return Grant{Unit: "none"}
	}
}

// userToolFile mirrors the on-disk format. Built-in files omit kind; a user file
// must name one of the executable kinds.
type userToolFile struct {
	Name        string           `toml:"name"`
	Brief       string           `toml:"brief"`
	Kind        ToolKind         `toml:"kind"`
	Description string           `toml:"description"`
	Schema      string           `toml:"schema"`
	OutputLimit int              `toml:"output_limit"`
	Params      map[string]Param `toml:"params"`
	CLI         *CLISpec         `toml:"cli"`
	HTTP        *HTTPSpec        `toml:"http"`
}

func loadUserTools(dir string) (map[string]ResolvedTool, error) {
	return loadToolDir(dir, LayerUser, false)
}

// loadProjectTools loads <workspace>/.pai/tools only when workspace is inside a
// configured trusted project root. The directory and every definition must stay
// inside that workspace; a symlink cannot turn a trusted project into an
// arbitrary file reader.
func loadProjectTools(workspace string, trustedProjects []string) (map[string]ResolvedTool, error) {
	ws, trusted := trustedWorkspace(workspace, trustedProjects)
	if !trusted {
		return map[string]ResolvedTool{}, nil
	}
	dir := filepath.Join(ws, ".pai", "tools")
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]ResolvedTool{}, nil
		}
		return nil, fmt.Errorf("resolve project tools dir %s: %w", dir, err)
	}
	if !within(ws, resolved) {
		return nil, fmt.Errorf("project tools dir %s escapes workspace %s", resolved, ws)
	}
	return loadToolDir(resolved, LayerProject, true)
}

func loadToolDir(dir string, layer ToolLayer, rejectSymlinks bool) (map[string]ResolvedTool, error) {
	out := make(map[string]ResolvedTool)
	if dir == "" {
		return out, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("read %s tools dir %s: %w", layer, dir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".toml")
		path := filepath.Join(dir, e.Name())
		if rejectSymlinks {
			info, err := os.Lstat(path)
			if err != nil {
				return nil, fmt.Errorf("stat %s tool %s: %w", layer, path, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("%s tool %s must not be a symlink", layer, path)
			}
		}
		t, err := parseToolFile(path, base, layer)
		if err != nil {
			return nil, err
		}
		if isBuiltinTool(t.Name) {
			return nil, fmt.Errorf("%s tool %q (%s) shadows a built-in name", layer, t.Name, path)
		}
		if _, seen := out[t.Name]; seen {
			return nil, fmt.Errorf("duplicate %s tool %q (second definition at %s)", layer, t.Name, path)
		}
		out[t.Name] = t
	}
	return out, nil
}

func parseToolFile(path, fileBase string, layer ToolLayer) (ResolvedTool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ResolvedTool{}, fmt.Errorf("read %s tool %s: %w", layer, path, err)
	}
	var raw userToolFile
	if err := toml.Unmarshal(data, &raw); err != nil {
		return ResolvedTool{}, fmt.Errorf("parse %s tool %s: %w", layer, path, err)
	}
	name := strings.TrimSpace(raw.Name)
	if name == "" {
		name = fileBase
	}
	if name != fileBase {
		return ResolvedTool{}, fmt.Errorf("%s tool %s: name %q must match filename %q", layer, path, name, fileBase)
	}
	if err := validateToolName(name); err != nil {
		return ResolvedTool{}, fmt.Errorf("%s tool %s: %w", layer, path, err)
	}
	switch raw.Kind {
	case ToolCLI, ToolHTTP:
	case "":
		return ResolvedTool{}, fmt.Errorf("%s tool %s: kind is required (cli or http)", layer, path)
	default:
		return ResolvedTool{}, fmt.Errorf("%s tool %s: unknown kind %q (cli or http)", layer, path, raw.Kind)
	}
	if raw.Kind == ToolCLI && raw.HTTP != nil {
		return ResolvedTool{}, fmt.Errorf("%s tool %s: cli tool must not define [http]", layer, path)
	}
	if raw.Kind == ToolHTTP && raw.CLI != nil {
		return ResolvedTool{}, fmt.Errorf("%s tool %s: http tool must not define [cli]", layer, path)
	}

	params := orderedParams(raw.Params)
	if err := validateParams(params); err != nil {
		return ResolvedTool{}, fmt.Errorf("%s tool %s: %w", layer, path, err)
	}
	paramMap := make(map[string]Param, len(params))
	for _, p := range params {
		paramMap[p.Name] = p
	}
	if raw.Kind == ToolCLI {
		if err := validateCLI(raw.CLI, paramMap); err != nil {
			return ResolvedTool{}, fmt.Errorf("%s tool %s: %w", layer, path, err)
		}
		raw.CLI.Command = strings.TrimSpace(raw.CLI.Command)
		raw.CLI.Dir = strings.TrimSpace(raw.CLI.Dir)
	} else {
		if err := validateHTTP(raw.HTTP, paramMap); err != nil {
			return ResolvedTool{}, fmt.Errorf("%s tool %s: %w", layer, path, err)
		}
		raw.HTTP.Method = strings.ToUpper(strings.TrimSpace(raw.HTTP.Method))
		raw.HTTP.URL = strings.TrimSpace(raw.HTTP.URL)
		if raw.HTTP.MaxBytes == 0 {
			raw.HTTP.MaxBytes = 32 << 10
		}
	}
	if raw.OutputLimit < 0 || raw.OutputLimit > maxOutputLimit {
		return ResolvedTool{}, fmt.Errorf("%s tool %s: output_limit must be between 0 and %d", layer, path, maxOutputLimit)
	}
	if utf8.RuneCountInString(raw.Brief) > maxBriefChars {
		return ResolvedTool{}, fmt.Errorf("%s tool %s: brief is longer than %d characters", layer, path, maxBriefChars)
	}
	if len(raw.Description) > maxDescriptionBytes {
		return ResolvedTool{}, fmt.Errorf("%s tool %s: description is longer than %d bytes", layer, path, maxDescriptionBytes)
	}

	// User tools may be trusted persistently; project tools are session-only and
	// excluded from the trust-all switch.
	persistent := layer == LayerUser
	rt := ResolvedTool{
		Name:        name,
		Brief:       raw.Brief,
		Description: raw.Description,
		Schema:      raw.Schema,
		Kind:        raw.Kind,
		Layer:       layer,
		Source:      path,
		Params:      params,
		CLI:         raw.CLI,
		HTTP:        raw.HTTP,
		OutputLimit: raw.OutputLimit,
		Grant: Grant{
			Unit:       "tool",
			Key:        name,
			Persistent: persistent,
			TrustAll:   persistent,
		},
	}
	if rt.Schema == "" {
		rt.Schema = generatedSchema(params)
	}
	rt.DefinitionHash, err = hashTool(rt)
	if err != nil {
		return ResolvedTool{}, fmt.Errorf("%s tool %s: hash: %w", layer, path, err)
	}
	return rt, nil
}

// trustedWorkspace resolves workspace and returns it when it lies inside any
// configured project root. Both sides are symlink-resolved, so a link cannot
// make an untrusted checkout look trusted.
func trustedWorkspace(workspace string, roots []string) (string, bool) {
	if workspace == "" || len(roots) == 0 {
		return "", false
	}
	ws, err := filepath.Abs(workspace)
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(ws); err == nil {
		ws = resolved
	}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		root = expandTilde(root)
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		if within(abs, ws) {
			return ws, true
		}
	}
	return ws, false
}

func expandTilde(path string) string {
	if path == "~" {
		return paths.Home()
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		home := paths.Home()
		if home == "" {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func validateToolName(name string) error {
	if !toolNameRe.MatchString(name) {
		return fmt.Errorf("invalid tool name %q (use ^[a-z][a-z0-9_]{0,40}$)", name)
	}
	return nil
}

func orderedParams(m map[string]Param) []Param {
	if len(m) == 0 {
		return nil
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	params := make([]Param, 0, len(names))
	for _, name := range names {
		p := m[name]
		p.Name = name
		params = append(params, p)
	}
	return params
}

func validateParams(params []Param) error {
	seen := make(map[string]bool, len(params))
	for _, p := range params {
		if err := validateToolName(p.Name); err != nil {
			return fmt.Errorf("parameter %q: %w", p.Name, err)
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate parameter %q", p.Name)
		}
		seen[p.Name] = true
		switch p.Type {
		case ParamString:
			if p.Min != nil || p.Max != nil {
				return fmt.Errorf("parameter %q: min/max are only valid for numbers", p.Name)
			}
			if p.MaxLength < 0 {
				return fmt.Errorf("parameter %q: max_length must be non-negative", p.Name)
			}
			if p.Pattern != "" {
				if _, err := regexp.Compile(p.Pattern); err != nil {
					return fmt.Errorf("parameter %q: invalid pattern: %w", p.Name, err)
				}
			}
		case ParamInteger, ParamNumber:
			if p.MaxLength != 0 || p.Pattern != "" {
				return fmt.Errorf("parameter %q: max_length/pattern are only valid for strings", p.Name)
			}
			if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
				return fmt.Errorf("parameter %q: min is greater than max", p.Name)
			}
		case ParamBoolean:
			if p.Min != nil || p.Max != nil || p.MaxLength != 0 || p.Pattern != "" {
				return fmt.Errorf("parameter %q: boolean has no min/max/length/pattern", p.Name)
			}
		case ParamEnum:
			if len(p.Values) == 0 {
				return fmt.Errorf("parameter %q: enum needs values", p.Name)
			}
			seenValues := make(map[string]bool, len(p.Values))
			for _, v := range p.Values {
				if v == "" {
					return fmt.Errorf("parameter %q: enum values must be non-empty", p.Name)
				}
				if seenValues[v] {
					return fmt.Errorf("parameter %q: duplicate enum value %q", p.Name, v)
				}
				seenValues[v] = true
			}
		default:
			return fmt.Errorf("parameter %q: unknown type %q", p.Name, p.Type)
		}
		if p.Required && p.Default != nil {
			return fmt.Errorf("parameter %q: required and default are mutually exclusive", p.Name)
		}
		if p.Default != nil {
			if err := validateDefault(p); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateDefault(p Param) error {
	switch p.Type {
	case ParamString:
		s, ok := p.Default.(string)
		if !ok {
			return fmt.Errorf("parameter %q: default must be a string", p.Name)
		}
		if p.MaxLength > 0 && len(s) > p.MaxLength {
			return fmt.Errorf("parameter %q: default exceeds max_length", p.Name)
		}
		if p.Pattern != "" {
			re, _ := regexp.Compile(p.Pattern) // validated above
			if !re.MatchString(s) {
				return fmt.Errorf("parameter %q: default does not match pattern", p.Name)
			}
		}
	case ParamInteger:
		if _, ok := p.Default.(int64); !ok {
			return fmt.Errorf("parameter %q: default must be an integer", p.Name)
		}
		if err := checkNumericRange(p, float64(p.Default.(int64))); err != nil {
			return err
		}
	case ParamNumber:
		switch v := p.Default.(type) {
		case int64:
			if err := checkNumericRange(p, float64(v)); err != nil {
				return err
			}
		case float64:
			if err := checkNumericRange(p, v); err != nil {
				return err
			}
		default:
			return fmt.Errorf("parameter %q: default must be a number", p.Name)
		}
	case ParamBoolean:
		if _, ok := p.Default.(bool); !ok {
			return fmt.Errorf("parameter %q: default must be a boolean", p.Name)
		}
	case ParamEnum:
		s, ok := p.Default.(string)
		if !ok {
			return fmt.Errorf("parameter %q: default must be a string", p.Name)
		}
		found := false
		for _, v := range p.Values {
			if v == s {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("parameter %q: default %q is not an enum value", p.Name, s)
		}
	}
	return nil
}

func checkNumericRange(p Param, v float64) error {
	if p.Min != nil && v < *p.Min {
		return fmt.Errorf("parameter %q: default is below min", p.Name)
	}
	if p.Max != nil && v > *p.Max {
		return fmt.Errorf("parameter %q: default is above max", p.Name)
	}
	return nil
}

func validateCLI(c *CLISpec, params map[string]Param) error {
	if c == nil {
		return errors.New("cli tool needs a [cli] table")
	}
	command := strings.TrimSpace(c.Command)
	if command == "" {
		return errors.New("[cli] command is required")
	}
	if strings.ContainsAny(command, " \t\r\n") {
		return fmt.Errorf("[cli] command %q must be a single executable name or path", command)
	}
	if strings.ContainsAny(command, "{}") {
		return errors.New("[cli] command may not contain parameter placeholders")
	}
	if c.Dir != "" {
		clean := filepath.Clean(c.Dir)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("[cli] dir %q must stay inside the workspace", c.Dir)
		}
	}
	if c.TimeoutSeconds < 0 || c.TimeoutSeconds > 24*60*60 {
		return errors.New("[cli] timeout_seconds must be between 0 and 86400")
	}
	for _, arg := range c.Args {
		if err := validatePlaceholders(arg, params, true); err != nil {
			return fmt.Errorf("[cli] arg %q: %w", arg, err)
		}
	}
	for key, value := range c.Env {
		if key == "" {
			return errors.New("[cli] env keys must be non-empty")
		}
		if strings.HasPrefix(value, "env:") && strings.TrimPrefix(value, "env:") == "" {
			return fmt.Errorf("[cli] env %q: env: reference needs a name", key)
		}
	}
	return nil
}

func validateHTTP(h *HTTPSpec, params map[string]Param) error {
	if h == nil {
		return errors.New("http tool needs an [http] table")
	}
	method := strings.ToUpper(strings.TrimSpace(h.Method))
	if method != "GET" && method != "POST" {
		return fmt.Errorf("[http] method %q must be GET or POST", h.Method)
	}
	// The destination is definition-owned: parameters may fill path/query/body,
	// never the scheme or host. Check the raw authority before url.Parse can
	// normalize or reject it.
	rest := h.URL
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	authority := rest
	if i := strings.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	if strings.ContainsAny(authority, "{}") {
		return errors.New("[http] url scheme/host may not contain parameter placeholders")
	}
	u, err := url.Parse(h.URL)
	if err != nil {
		return fmt.Errorf("[http] url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("[http] url scheme %q must be http or https", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("[http] url needs a host")
	}
	if err := validatePlaceholders(h.URL, params, true); err != nil {
		return fmt.Errorf("[http] url: %w", err)
	}
	for key, value := range h.Headers {
		if !validHeaderName(key) {
			return fmt.Errorf("[http] header name %q is not a valid token", key)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("[http] header %q must not contain a newline", key)
		}
		if strings.ContainsAny(value, "{}") {
			return fmt.Errorf("[http] header %q may not contain placeholders", key)
		}
	}
	if h.Auth != "" {
		fields := strings.Fields(h.Auth)
		if len(fields) < 2 {
			return fmt.Errorf("[http] auth %q must be 'bearer ENV' or 'header NAME ENV'", h.Auth)
		}
		switch fields[0] {
		case "bearer":
			if len(fields) != 2 {
				return fmt.Errorf("[http] auth %q must be 'bearer ENV'", h.Auth)
			}
		case "header":
			if len(fields) != 3 {
				return fmt.Errorf("[http] auth %q must be 'header NAME ENV'", h.Auth)
			}
		default:
			return fmt.Errorf("[http] auth %q must start with 'bearer' or 'header'", h.Auth)
		}
	}
	if method == "GET" && len(h.BodyParams) > 0 {
		return errors.New("[http] GET must not declare body_params")
	}
	for _, name := range h.BodyParams {
		p, ok := params[name]
		if !ok {
			return fmt.Errorf("[http] body_params names unknown parameter %q", name)
		}
		// The body always includes a declared field, so absent-and-optional would
		// fail at run time with no way for the model to know.
		if !p.Required && p.Default == nil {
			return fmt.Errorf("[http] body_params %q must be required or have a default", name)
		}
	}
	for key, value := range h.BodyExtra {
		if key == "" {
			return errors.New("[http] body_extra keys must be non-empty")
		}
		if _, ok := params[key]; ok {
			return fmt.Errorf("[http] body_extra %q collides with a declared parameter", key)
		}
		if strings.ContainsAny(value, "{}") {
			return fmt.Errorf("[http] body_extra %q may not contain placeholders", key)
		}
	}
	if h.MaxBytes < 0 || h.MaxBytes > maxHTTPBytes {
		return fmt.Errorf("[http] max_bytes must be between 0 and %d", maxHTTPBytes)
	}
	switch h.Format {
	case "", "json", "text":
	default:
		return fmt.Errorf("[http] format %q must be json or text", h.Format)
	}
	return nil
}

// validHeaderName accepts the RFC 7230 token characters. net/http would only
// report an invalid name at send time, after the user already approved, so this
// is a load error.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)):
		default:
			return false
		}
	}
	return true
}

func validatePlaceholders(s string, params map[string]Param, requireValue bool) error {
	for _, m := range placeholderRe.FindAllStringSubmatch(s, -1) {
		name := m[1]
		if err := validateToolName(name); err != nil {
			return fmt.Errorf("invalid placeholder %q", m[0])
		}
		p, ok := params[name]
		if !ok {
			return fmt.Errorf("placeholder %q names an unknown parameter", m[0])
		}
		if requireValue && !p.Required && p.Default == nil {
			return fmt.Errorf("placeholder %q names an optional parameter without a default", m[0])
		}
	}
	return nil
}

// generatedSchema renders the payload shape shown in the prompt head. It is
// deliberately compact; the executor validates the same Param definitions.
func generatedSchema(params []Param) string {
	if len(params) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteString("{")
	for i, p := range params {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%q:", p.Name)
		switch p.Type {
		case ParamString:
			b.WriteString(`"<string`)
			if p.Required {
				b.WriteString(", required")
			}
			if p.Default != nil {
				fmt.Fprintf(&b, ", default=%v", formatDefault(p.Default))
			}
			if p.MaxLength > 0 {
				fmt.Fprintf(&b, ", max_length=%d", p.MaxLength)
			}
			if p.Pattern != "" {
				fmt.Fprintf(&b, ", pattern=%s", p.Pattern)
			}
			b.WriteString(`>"`)
		case ParamInteger, ParamNumber:
			fmt.Fprintf(&b, "<%s", p.Type)
			if p.Default != nil {
				fmt.Fprintf(&b, "=%v", formatDefault(p.Default))
			}
			if p.Min != nil || p.Max != nil {
				b.WriteString(", ")
				if p.Min != nil {
					fmt.Fprintf(&b, "%v", *p.Min)
				} else {
					b.WriteString("-inf")
				}
				b.WriteString("..")
				if p.Max != nil {
					fmt.Fprintf(&b, "%v", *p.Max)
				} else {
					b.WriteString("+inf")
				}
			}
			b.WriteString(">")
		case ParamBoolean:
			b.WriteString("<boolean")
			if p.Default != nil {
				fmt.Fprintf(&b, "=%v", formatDefault(p.Default))
			}
			b.WriteString(">")
		case ParamEnum:
			fmt.Fprintf(&b, `"<enum: %s>"`, strings.Join(p.Values, "|"))
		}
	}
	b.WriteString("}")
	return b.String()
}

func formatDefault(v any) string {
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprint(v)
}

// canonicalTool is the hash input. It deliberately excludes Layer and Source:
// identical definitions in different layers have the same semantics, while a
// changed argv/URL/limit/grant changes the hash.
type canonicalTool struct {
	Name        string    `json:"name"`
	Brief       string    `json:"brief"`
	Description string    `json:"description"`
	Schema      string    `json:"schema"`
	Kind        ToolKind  `json:"kind"`
	Params      []Param   `json:"params,omitempty"`
	CLI         *CLISpec  `json:"cli,omitempty"`
	HTTP        *HTTPSpec `json:"http,omitempty"`
	OutputLimit int       `json:"output_limit,omitempty"`
	Grant       Grant     `json:"grant"`
}

func hashTool(t ResolvedTool) (string, error) {
	payload, err := json.Marshal(canonicalTool{
		Name:        t.Name,
		Brief:       t.Brief,
		Description: t.Description,
		Schema:      t.Schema,
		Kind:        t.Kind,
		Params:      t.Params,
		CLI:         t.CLI,
		HTTP:        t.HTTP,
		OutputLimit: t.OutputLimit,
		Grant:       t.Grant,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
