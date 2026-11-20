# cli

Composition root for terminal commands and the HTTP service.
See the [module map](../organization.md) for dependency rules.

## Entry points

- `Run` dispatches commands and wires configuration, providers, the role loop,
  terminal adapters, persistence, and cancellation.
- `CliFlags` / `GetFlags` describe and parse agent-run flags.
- `command.go` owns subcommand dispatch; `config.go`, `role.go`, `tool.go`,
  `service.go`, `group.go`, and `session.go` implement the corresponding command
  families.
- `service.go` creates resident sessions from a validated manifest, lists their
  state, status, watchers, and next wake, edits a stopped service's manifest,
  stops or starts them, and deletes one with its history (`svc` is an alias).
  Stop/start/edit/delete only work while the server is down, because they need
  the store lock; the web API exposes the same transitions. `service show` prints
  the queue with its trace and hop count, so one chain of cross-session work can
  be followed from either the terminal or the browser.
  Both delete verbs (`service rm`, `session rm`) go through `rmGuard`: a session
  that leads a group is refused until the group is deleted or its lead changes,
  and any board work the session held is released as orphaned first so another
  member can pick it up. `runner.Manager.Delete` applies the same rule while the
  server is running, which is why the rule lives in both places.
- `group.go` manages the group directory: create from a manifest, list, show the
  shared goal, members, their in-group descriptions, and board, add or remove a
  member, set a member's description (`pai group intro`, empty clears it), start
  or stop every member, and delete the directory without touching the sessions.
  Like `service.go` it needs the store lock. Membership is enforced by `runner`
  through the optional `GroupBackend` port, which `servebackend.go` forwards to
  the store; the board is forwarded the same way through `TaskBackend`. The web
  edits the same directory through `GroupAdminBackend`, so a group can be created
  while the server runs; name resolution lives in `session.GroupFromNames` so the
  manifest and the form agree on what a member may be. `group show` also lists the
  newest activity rows; `groupactivity.go` builds that timeline (events plus parsed
  `inform`/`done` turns) for both the local and the remote path.
  - `remote.go` is the read fallback: while `pai serve` owns the store lock, the
  read commands (`session ls/show`, `service ls/show`, `group ls/show`) query the
  running server through `conn` instead of failing. Writes still require the lock,
  so the error they print says so.
- A service is run by `pai serve`, which registers every `mode=service` session
  with the scheduler at startup — except one persisted as `stopped`.
- The `serve*.go` files compose the session backend, `runner.Manager`, and
  `web.New`: `serve.go` owns flags, the entry point, and shutdown; `servebackend.go`
  implements the runner backend; `serveauth.go` handles origins and tokens;
  `serveactivity.go` writes the activity log, and `servediag.go` writes the
  detailed `--debug-log` stream (probe verdicts, activation decisions, model spend,
  and bounded parser format failures with finish reason and token counts).
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
