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
- `Store.SetOverride`/`Store.DeleteOverride` keep a small **override layer** over
  the turns: at most one `Override` per `(session, seq)`, either a replacement
  (`core.OverrideReplace`) or a drop (`core.OverrideDrop`). The layer is keyed by
  the same `seq` as the turns and cascades with them, so a truncate or rename
  takes its overrides along. It never rewrites a stored turn — it only changes
  what a run sends. `Get` loads it alongside the turns; a session with none leaves
  `Session.Overrides` nil.
- `Store.SetUsage` stores the last request's reported prompt size next to the
  estimate made for that same prompt. `Meta.TokenScale` is their ratio (clamped to
  `[0.5, 2]`, and 1 until something has reported), which is how `chat` calibrates
  its byte-based token estimate against the provider's real tokenizer. The pair is
  accounting, not activity: `SetUsage` leaves `updated_at` alone so the session list
  does not reorder on every turn.
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

## Storage backend

Sessions live in the XDG data directory (`$XDG_DATA_HOME/pai/`, or
`~/.local/share/pai/`). Storage uses pure-Go SQLite (`modernc.org/sqlite`), so no
C toolchain is involved and the release binaries stay cross-compilable.
`make build` produces a stripped binary (~13 MB).

SQLite is the only backend. Earlier releases could be built with `-tags filestore`
to keep sessions as JSONL files instead; that build option is gone, so a
`sessions/` directory of `.jsonl` files from such a build is left on disk but no
longer read.
