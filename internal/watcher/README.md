# internal/watcher

Evaluates one resident service's host-side check.

See the [module map](../organization.md) for layering.

## The probe contract

A watcher with a `command` is a **predicate**, not a sensor. It runs in the
session's workspace with a timeout and prints a decision on its first line:

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

`timer` watchers have no command and fire every interval; their `prompt` is the
wake text (empty falls back to `scheduled tick`). Both kinds may carry a `prompt`:
for a command probe it is extra guidance appended after the output when the probe
fires, so the model knows what to do with the observation.

## Latching and outstanding work

`Probe` receives the previous persisted state and returns the next one:

| field | meaning |
|---|---|
| `fired` | when the model was last woken (unix seconds) |
| `handled` | when the model last finished with that wake |
| `reported` | whether this continuous episode has already fired |
| `acknowledged` | whether that fire has been handled |
| `error` | the last probe failure, so a distinct failure is news once |

The rules:

- no `repeat_after` — **once per episode**: the latch clears when the predicate
  goes false, so a level condition never wakes the model on every probe by
  accident.
- `repeat_after` — a reminder is due once `now - max(fired, handled)` reaches it.
  Counting from handling means a long activation does not come back with a
  reminder it has already earned by working.
- a probe failure keeps the latch, so a transient error does not restart the
  episode. It wakes the model once per distinct message.
- a false sample forgets the whole document: the next true sample is a new
  incident and fires immediately.

Debouncing a flappy signal belongs in the probe script, where the condition's
shape is known; the manifest owns no second timing knob for it.

`reported && !acknowledged` is the host's notion of **outstanding work**, and it
is derived rather than queued here: the durable event queue stays the source of
truth. The runner marks a watcher handled only after it acknowledges the wake, so
a nacked (retried) activation leaves its item open. Probing continues while an
item is open; without `repeat_after` it does not fire again, and a reminder that
is due replaces the still-pending event rather than queueing a second one.

The summary is capped at 512 bytes and the stored state is one JSON object, which
fits the store's single `last_state` column. `Result.NextRun` is `now + interval`
(minimum `config.MinWatchInterval`, 10s); the runner persists it and asks the
scheduler for the earliest of the pass.

`MarkHandled` and `Open` are the two host-facing operations on that document: the
first closes a fire, the second reads whether one is still open.

## Boundaries

This package knows no sessions, scheduling, or model. It imports `config` for the
spec and `tool` only to run the bounded command. The command is **not** confirmed
at probe time: creation approved it once, and a resident check cannot wait for a
human.

Suggested validation from the repository root: `go test ./internal/watcher`.
