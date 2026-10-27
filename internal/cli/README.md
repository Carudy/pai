# cli

Composition root for terminal commands and the HTTP service.
See the [module map](../organization.md) for dependency rules.

## Entry points

- `Run` dispatches commands and wires configuration, providers, the role loop,
  terminal adapters, persistence, and cancellation.
- `CliFlags` / `GetFlags` describe and parse agent-run flags.
- `command.go` owns subcommand dispatch; `config.go`, `role.go`, `tool.go`,
  `service.go`, and `session.go` implement the corresponding command families.
- `service.go` creates resident sessions from a validated manifest, lists their
  state, status, watchers, and next wake, edits a stopped service's manifest,
  stops or starts them, and deletes one with its history (`svc` is an alias).
  Stop/start/edit/delete only work while the server is down, because they need
  the store lock; the web API exposes the same transitions.
transitions. A service is run by `pai serve`, which registers every
  `mode=service` session with the scheduler at startup — except one persisted as
  `stopped`.
- The `serve*.go` files compose the session backend, `runner.Manager`, and
  `web.New`: `serve.go` owns flags, the entry point, and shutdown; `servebackend.go`
  implements the runner backend; `serveauth.go` handles origins and tokens;
  `serveactivity.go` writes the activity log, and `servediag.go` writes the
  detailed `--debug-log` stream (probe verdicts, activation decisions, model spend).
- `conn.go` is the terminal client for a running server; it wires `conn.Client`
  to `tui.NewLineObserver` and never opens the store. Its target resolves from
  flags, then `PAI_SERVE_*`, then the `[conn]` section of `config.toml`.
- Served sessions use their persisted workspace; new sessions default to the
  starting cwd. Each runtime gets its own working directory, without `os.Chdir`.

## Boundaries

Only this package imports the `tui` and `session` adapters to wire the application.
Keep protocol and tool behavior in their owning packages; generate role/tool
names from `prompts` rather than maintaining separate lists here.
`tool.go` is a thin inspector: resolution, validation, and argv rendering stay in
`prompts`, and nothing is executed by `pai tool check`.

Suggested validation from the repository root:

```sh
go test ./internal/cli
```
