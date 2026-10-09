# cli

Composition root for terminal commands and the HTTP service.
See the [module map](../organization.md) for dependency rules.

## Entry points

- `Run` dispatches commands and wires configuration, providers, the role loop,
  terminal adapters, persistence, and cancellation.
- `CliFlags` / `GetFlags` describe and parse agent-run flags.
- `command.go` owns subcommand dispatch; `config.go`, `role.go`, and
  `session.go` implement the corresponding command families.
- `serve.go` composes the session backend, `runner.Manager`, and `web.New`,
  validates bind/authentication settings, and owns shutdown.
- Served sessions use their persisted workspace; new sessions default to the
  starting cwd. Each runtime gets its own working directory, without `os.Chdir`.

## Boundaries

Only this package imports the `tui` and `session` adapters to wire the application.
Keep protocol and tool behavior in their owning packages; generate role/tool
names from `prompts` rather than maintaining separate lists here.

Suggested validation from the repository root:

```sh
go test ./internal/cli
go test -tags filestore ./internal/cli
```
