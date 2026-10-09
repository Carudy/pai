# session

Durable conversation storage behind a backend-independent interface.
See the [module map](../organization.md) for the storage adapter boundary.

## Entry points

- `Open` selects pure-Go SQLite by default, or JSONL with `-tags filestore`.
  The backends use different on-disk formats; one does not expose the other's sessions.
- `Store` creates, loads, lists, appends, renames, and deletes sessions.
- `Meta` includes role, model, cwd, title, timestamps, and turn count;
  `Session` pairs metadata with ordered `core.Turn` history.
- `Store.SetModel` and `Store.SetRole` change saved model/role metadata without
  changing history; both backends implement them.
- `NewRecorder` implements `core.Recorder`; `controller.go` adapts session
  operations to the loop's storage port.

## Ownership and boundaries

`Open` takes exclusive, nonblocking OS ownership of the entire data directory,
shared by both backends, including readers; contention returns `ErrLocked`.
Supported Unix systems use `flock`, Windows uses `LockFileEx`, and unsupported
platforms fail closed. `Close` or process exit releases ownership. The persistent
`sessions.lock` file is not a stale lock: never remove it while a store is open.
Only `cli` imports this adapter; the loop uses `core` ports instead.

Suggested validation from the repository root, covering both backends:

```sh
go test ./internal/session
go test -tags filestore ./internal/session
```
