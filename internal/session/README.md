# session

Durable conversation storage behind a backend-independent interface.
See the [module map](../organization.md) for the storage adapter boundary.

## Entry points

- `Open` opens the pure-Go SQLite store (`modernc.org/sqlite`, so builds stay
  cgo-free). `Backend` names the engine for display.
- `Store` creates, loads, lists, appends, renames, and deletes sessions.
- `Meta` includes role, model, cwd, title, timestamps, and turn count;
  `Session` pairs metadata with ordered `core.Turn` history.
- `Store.SetModel`, `Store.SetRole`, `Store.SetWorkingDir`, and `Store.Truncate`
  change saved metadata or history without touching the rest; `Rename`/`Delete`
  round out the set.
- `NewRecorder` implements `core.Recorder`; `controller.go` adapts session
  operations to the loop's storage port.

## Ownership and boundaries

`Open` takes exclusive, nonblocking OS ownership of the entire data directory,
including readers; contention returns `ErrLocked`. Supported Unix systems use
`flock`, Windows uses `LockFileEx`, and unsupported platforms fail closed. `Close`
or process exit releases ownership. The persistent `sessions.lock` file is not a
stale lock: never remove it while a store is open.
Only `cli` imports this adapter; the loop uses `core` ports instead.

Timestamps are stored with one-second resolution, so ordering ties are broken by
name to keep `List` and `Latest` stable between runs.

Suggested validation from the repository root:

```sh
go test ./internal/session
```
