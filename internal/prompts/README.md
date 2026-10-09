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
- `roles/*.toml` define intros, capabilities, and project-context files;
  `tools/*.toml` describe tool behavior and payload shapes.

## Boundaries

Definitions do not execute tools. Implementations and capability coverage checks
live in `role`; a role may only reference implemented tools.
Keep payload schemas consistent with handlers and use discovery APIs, not
hardcoded role/tool lists. User role overrides are distinct from intro overrides
loaded by `config` from `prompts.toml`.

Suggested consumer validation from the repository root:

```sh
go test ./internal/chat ./internal/role
go test -tags filestore ./internal/chat ./internal/role
```
