# role

The single agent loop, parameterized by a data-defined role.
See the [module map](../organization.md) for the loop's dependencies and ports.

## Entry points

- `Run` drives model actions, tools, questions, and completion.
- `Runtime` holds per-run clients, semantic ports, transcript state, and optional
  persistence; configuration remains in `config.UserConfig`.
- Set `Runtime.WorkingDir` before running and keep it immutable during the run.
  It scopes local tools and project instructions; empty preserves process cwd.
  Separate runtimes can use separate workspaces without changing process cwd.
- `Runtime.Interrupt` cancels the current step rather than the entire runtime.
- `tools.go` owns tool dispatch and capability coverage; `filetools.go` handles
  file operations. `command.go` declares in-session commands and generated help.

## Boundaries

Roles differ by TOML, not separate loops. Tool handlers validate model payloads
and return bad input as tool-error observations so the model can self-correct.
Approval belongs here via `core.Prompter`, not the execution layer.
Use `core` ports instead of importing terminal or session adapters.

Suggested validation from the repository root:

```sh
go test ./internal/role
go test -tags filestore ./internal/role
```
