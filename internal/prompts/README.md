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
