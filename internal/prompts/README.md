# prompts

Role and tool definitions as TOML data, embedded for installed binaries.
See the [module map](../organization.md) for how the loop consumes them.

## Entry points

- `RoleNames` and `ToolNames` provide sorted discovery for help and validation.
- `ReadRole` returns definition bytes and a source label. Resolution prefers
  user files in `UserRolesDir()`, then source-checkout files, then embedded data.
- `UserRolesDir` is the `roles` directory under PAI's resolved config directory;
  users can add or override roles without rebuilding.
- `ReadTool` tries source-checkout definitions before embedded tools.
- `LoadRole` / `LoadTool` resolve **and parse** a definition into `Role` / `Tool`.
  Parsing lives here so this package owns the TOML format: consumers work with
  typed values and never import a TOML decoder.
- `ResolveTools` builds an `EffectiveSet` of `ResolvedTool` values from a role's
  declared tools, `role-extra.toml` tools, the user tool directory, and (only in
  a trusted workspace) `.pai/tools`. It carries layer, source, definition hash,
  execution kind, and the approval unit.
- `UserToolsDir` is `~/.config/pai/tools`; `ResolveToolIn` resolves one name for
  inspection and `pai tool` dry runs, and `ProjectToolNames` lists the project
  layer visible for a workspace.
- `ResolvedTool.ValidatePayload` / `RenderArgs` validate a JSON object and
  substitute typed values into a CLI argv without a shell.
- `roles/*.toml` define intros, capabilities, and project-context files;
  `tools/*.toml` describe tool behavior and payload shapes.
- The built-in `latex` role handles scientific editing, bibliography evidence,
  and non-interactive LaTeX validation. It enables `papersearch` alongside
  `websearch`; `devops` deliberately excludes academic search. A user's
  `roles/latex.toml` still overrides the built-in definition.

## Boundaries

Definitions do not execute tools. Implementations and capability coverage checks
live in `role`; a role may only reference implemented tools. The resolved tool
name is the role's declaration, not a `name` field inside the file, because that
declaration is what the handler registry and capability check use.
Keep payload schemas consistent with handlers and use discovery APIs, not
hardcoded role/tool lists. User role overrides (`~/.config/pai/roles/`) are
distinct from the extra intro text `config` loads from `role-extra.toml`.

Suggested consumer validation from the repository root:

```sh
go test ./internal/chat ./internal/role
```

## Custom tools

A user tool is a TOML file in `~/.config/pai/tools/<name>.toml`. The filename is
its identity; a `name` field may be present but must match. Built-in names are
reserved, collisions are a load error, and a role still has to list the tool
before the model can call it.

```toml
name  = "run_eval"
brief = "run one evaluation dataset"
kind  = "cli"
description = "Run scripts/run_eval.py in the eval checkout."

[params]
dataset = { type = "string",  required = true }
folds   = { type = "integer", default = 5, min = 2, max = 10 }

[cli]
command = "uv"
args    = ["run", "scripts/run_eval.py", "--dataset", "{dataset}", "--folds", "{folds}"]
dir     = "."
```

- `kind = "cli"` runs an argv directly: no shell, no word splitting. A value
  containing spaces, `;`, or `$(...)` remains one argument.
- Parameters are declared under `[params]`; the prompt schema is generated from
  them. Every placeholder in `args` must name a required parameter or one with a
  default.
- `kind = "http"` pins a method and a literal host; path/query placeholders are
  escaped by position, a POST body is an encoded object from `body_params` plus
  `body_extra`, and `auth` names an environment variable. `prompts` renders the
  request but performs no network I/O; `tool.ExecuteHTTP` sends it with a
  header deadline, a total deadline, a byte cap, and no redirects.
- A workspace under one of `[tool] trusted_projects` adds its
  `.pai/tools/*.toml` definitions to the effective set of every role used there.
  The workspace and roots are symlink-resolved before the containment check, and
  a symlinked tools directory or definition is rejected. A project name that
  collides with a built-in or user tool is a load error.
- Trust is by tool name (`[tool] trusted_tools`), not by the executable or
  endpoint. User tools may persist a grant; a project tool's grant is
  session-only (`Grant.Persistent` and `Grant.TrustAll` are false for
  `LayerProject`), so it is never written to `trusted_tools` and the session
  trust-all switch does not auto-approve it. `pai tool ls`, `pai tool show`, and
  `pai tool check` inspect and dry run definitions without executing them.

Add a tool to a built-in role in `role-extra.toml`:

```toml
[devops]
tools = ["run_eval"]
```

## Extending a role

Append your own instructions to any role's intro with
`~/.config/pai/role-extra.toml`:

```toml
[devops]
text = """
You are a senior SRE. Always explain why before running commands.
"""
```

The text is appended to the role's *intro* under a `[User:]` marker — it never
replaces the intro, and it cannot change the response format, which is fixed by
the app. To change more than that (a different intro or tool set), define a
custom role instead; see the built-in and custom roles in
[`internal/role/README.md`](../role/README.md).

> **Renamed:** `~/.config/pai/prompts.toml` (with `prompt`/`additional`) is no
> longer read. Move its text to `role-extra.toml` — it is now always appended.

### On-demand guides (a poor-man's "skills")

`text` is loaded on every request, so keep it short and let it *point* at detail
only when it is needed. The cheapest on-demand guide is to name a file in the
role intro and let the model `read` it when relevant:

```toml
[devops]
text = """
Before triaging a Kubernetes incident, read docs/guides/k8s-triage.md.
"""
```

The pointer is always present; the guide's body enters the context only when the
model reads it. Reads still follow the usual path-trust rules.
