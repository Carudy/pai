# internal/watcher

Evaluates one resident service's host-side check.

See the [module map](../organization.md) for layering.

## The probe contract

A `command` watcher is a **predicate**, not a sensor. It runs in the session's
workspace with a timeout and prints a decision on its first line:

| stdout | meaning |
|---|---|
| `1` / `true` | the condition holds |
| `0` / `false` | it does not |
| text after the decision token | the summary handed to the model |
| more lines | appended to the summary |

Empty output, an unrecognised first token, a nonzero exit, or a timeout is a
**probe failure**, not a quiet "no". A bug in the script must be loud, because the
model cannot tell a broken probe from a calm world.

The decision lives in the command so the manifest owns no query language and a
probe can use whatever shell, Python, or `jq` is already on the host. The trade is
that PAI cannot validate the logic, only the decision format.

`timer` watchers have no command and fire every interval. A timer may carry a
`prompt`, which becomes the wake text (the built-in `_report` heartbeat uses one
to ask for a status report); a `command` watcher must not, because its output is
the summary.

## Debounce and latching

`Probe` receives the previous persisted state and returns the next one:

| field | meaning |
|---|---|
| `since` | when the predicate started holding (unix seconds) |
| `fired` | when the model was last woken |
| `reported` | whether this continuous episode has already fired |
| `error` | the last probe failure, so a distinct failure is news once |

The rules:

- `hold_for` — the predicate must keep holding for this long before firing. The
  first true sample opens the window, so detection latency is between `hold_for`
  and `hold_for + interval`.
- `repeat_after` — re-alert interval while the condition keeps holding.
- neither set — **once per episode**: the latch clears when the predicate goes
  false, so a level condition never wakes the model on every probe by accident.
- a probe failure keeps the window and latch, so a transient error neither
  restarts the debounce nor re-alerts.

The summary is capped at 512 bytes and the stored state is one JSON object, which
fits the store's single `last_state` column. `Result.NextRun` is `now + interval`
(minimum `config.MinWatchInterval`, 10s); the runner persists it and asks the
scheduler for the earliest of the pass.

## Boundaries

This package knows no sessions, scheduling, or model. It imports `config` for the
spec and `tool` only to run the bounded command. The command is **not** confirmed
at probe time: creation approved it once, and a resident check cannot wait for a
human.

Suggested validation from the repository root: `go test ./internal/watcher`.
