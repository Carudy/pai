# Runner integration contract

## Module overview

`runner` hosts bounded concurrent role runtimes independently of terminal or
HTTP transport. `New` creates a `Manager`; `Backend` supplies persistence and
per-worker preparation, while snapshots and subscriptions expose live state.
The composition root owns adapters and shutdown; runner does not import them.
See the [module map](../organization.md) for layering.

Suggested validation from the repository root: `go test ./internal/runner` and
`go test -tags filestore ./internal/runner`.

## Integration contract

Only the composition root supplies a `Backend`; runner imports no storage or transport adapters. Backend methods may run concurrently. `Prepare` returns a per-worker configuration/provider/recorder, resumed messages, and a cleanup function. It must return promptly (the requested interface has no context). Cleanup runs once before the worker's slot is released. Runner copies the configuration struct; treat its nested maps/slices as immutable or return independently owned values.

```go
m := runner.New(applicationContext, backend, maxLiveRuntimes)
defer m.Close()

err := m.Send(name, text)
err = m.Steer(name, text)
err = m.Cancel(name)
err = m.Reply(name, promptID, text, approve)
snapshot, err := m.Snapshot(name)
events, unsubscribe, err := m.Subscribe(name)
// Call unsubscribe when the client disconnects; it does not cancel the worker.
metas, err := m.List()
history, err := m.History(name, offset, limit)
```

- `Send` starts/reuses a worker and queues an instruction. Acceptance is asynchronous; preparation/run failures appear as `error` events followed by `stopped`. Persisted history is supplied by the backend, not an event replay buffer.
- Capacity caps **live** workers, including busy workers and pending approvals/questions. Idle (`awaiting`) workers are reused, or evicted and joined when a different session needs the slot. Full busy capacity returns `ErrCapacity`; there is no unbounded cross-session wait queue.
- Task and steering queues each hold 32 messages. `Send` never answers a model question. `Steer` is consumed at a safe point; while idle it becomes a normal instruction. `/new` and `/rename` are rejected, including case/whitespace variants; steering rejects slash commands altogether.
- `Cancel` interrupts the active role step and aborts its pending prompt. It does not abort idle instruction input or discard queued tasks. Cancel during preparation shuts that worker down. Worker lifetime follows the application context, not HTTP request contexts.
- `Reply` requires the pending prompt's string ID. `text` answers `ask`; `approve` answers `confirm`. Stale/duplicate replies return `ErrPrompt`. Prompt IDs are unique for the manager lifetime.
- `Subscribe` requires an existing live worker and immediately yields a `snapshot` event. Every event includes a snapshot with `name`, `state`, `queued`, optional `pending` (`id`, `kind`, `title`, optional `tool`), and optional `error`. Pending prompts survive disconnect/reconnect. Idle instruction requests have no pending prompt: render input based on `state == "awaiting"` and use `Send`.
- Confirmation prompts include `pending.tool` (and `prompt` event `data.tool`) when associated with a current tool call. This is an independently copied `core.ToolCall`, including exact `Detail` and `Diff` fields, so reconnecting clients can render the full approval preview without replaying earlier events. Tool fields retain their native Go/JSON names (`Name`, `Target`, `Detail`, `Reason`, `Trusted`, `Diff`). Questions and unrelated confirmations omit `tool`; completed or consumed tool calls are not reused.
- Events have `type`, optional `data`, and `snapshot`. Types: `snapshot`, `queued`, `busy`, `awaiting`, `prompt`, `prompt_replied`, `prompt_cancelled`, `reason`, `done`, `terminate`, `ask`, `user`, `session`, `reasoning`, `usage`, `tool_call`, `tool_output`, `tool_result`, `notice`, `output`, `separator`, `error`, `stopped`. Observer `ask` is informational; only `prompt` carries a replyable question/confirmation.
- `SubscribeActivities()` returns a global channel of `Activity{Name, Event}`, an idempotent unsubscribe function, and an error. It observes future events for all workers without a browser or per-session subscription, with no replay or initial snapshot. Each of at most 32 global subscriptions buffers 128 events and drops new events when full; it never invokes client callbacks or blocks a worker on output. Payloads and pending prompts are independently copied just as for per-session subscribers. `Close` closes activity channels after final worker events, allowing consumers to drain buffered events. Consumers decide filtering, privacy, formatting, and bounded shutdown waits.
- Each worker supports 32 subscribers with 64 buffered events each. A slow subscriber's channel closes; reconnect for a fresh snapshot and reload history. Unsubscribe is idempotent. There is no retained event log. `core` payloads retain their native field names.
- Finished/evicted worker entries are removed. `Snapshot`/`Subscribe` then return `ErrNotFound`; `History` remains available. `Close` is concurrent-safe, idempotent, cancels workers, and waits for cleanup. Backend/provider implementations must honor their contracts for shutdown to complete.
