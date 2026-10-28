# Runner integration contract

## Module overview

`runner` hosts bounded concurrent role runtimes independently of terminal or
HTTP transport. `New` creates a `Manager`; `Backend` supplies persistence and
per-worker preparation, while snapshots and subscriptions expose live state.
The composition root owns adapters and shutdown; runner does not import them.
See the [module map](../organization.md) for layering.

Suggested validation from the repository root: `go test ./internal/runner`.

## Resident scheduling (P1)

`Scheduler` is the central, transport-free timer/queue for resident sessions.
The host supplies `ServiceHost.Activate`; the scheduler guarantees at most one
activation per session, bounds concurrency with `maxActive`, folds an overdue
timer into a `core.Wake`, and queues events that arrive during an activation for
the next one. `ServiceInbox` is the worker side: the host pushes one wake,
`role.RunService` blocks in `Next`, and `Done` acknowledges it.

The manager is the production host. `StartServices` registers persisted services,
skipping one whose stored state is `stopped` (stopping is durable) and making a
session due now when durable events are already pending, so work queued before a
restart is not stranded behind a distant timer. `Activate`
prepares or reuses the resident worker, and `Send` on a service
becomes a durable `EventUser` instead of a chat task; a stopped service refuses
it with `ErrStopped`. `StopService` unregisters the service, cancels a live
activation (a parked worker included), and persists `stopped`; `StartService`
registers it due now. A service worker prepares
once, calls `role.RunService`, and stays `suspended` between wakes; `busy` marks
an activation. An activation carries at most `maxDueEvents` recovered events;
when the queue has more, it returns `next = now` so the scheduler keeps draining
rather than waiting for the next timer. The stored manifest's `goal` is installed with
`chat.RolePrompt.SetStanding`, so it sits in the prompt head on every wake and
survives compaction. `ServiceEventBackend` is the optional durable port for
append, ack/nack, due events, and the next wake; a backend without it runs an
ephemeral service.

Mail from another session is an `EventPeer`: `deliverMail` appends it durably and
wakes a registered target (a stopped target keeps it until it starts). The
receiver records it as a user turn, and the situation renderer omits it so the
model does not read it twice. It is data, never a trust grant; the sender needs
the `mail` tool, and only resident sessions are targets.

## Integration contract

Only the composition root supplies a `Backend`; runner imports no storage or transport adapters. Backend methods may run concurrently. `Prepare` returns a per-worker configuration/provider/recorder, resumed messages, and a cleanup function. It must return promptly (the requested interface has no context). Cleanup runs once before the worker's slot is released. Runner copies the configuration struct; treat its nested maps/slices as immutable or return independently owned values.

```go
m := runner.New(applicationContext, backend, maxLiveRuntimes)
defer m.Close()

err := m.Send(name, text)
err = m.Steer(name, text)
err = m.Cancel(name)
err = m.Reply(name, promptID, text, choice)
err = m.SetModel(name, "provider:model") // needs ModelBackend
err = m.SetRole(name, role)              // needs RoleSetter
err = m.SetWorkingDir(name, "/project")  // needs WorkingDirSetter
err = m.Rename(name, newName)            // needs RenameBackend
err = m.Rewind(name, keep, backup)        // needs RewindBackend
err = m.SetTrustAll(name, true)          // per-session auto-approval (in-memory)
trusted := m.TrustAll(name)
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
- `SetModel`, `SetRole`, `SetWorkingDir`, `Rename`, and `Rewind` change persisted session state. Each requires an idle session (a busy/starting worker, queued work, or a pending prompt returns `ErrBusy`) and retires the worker under a reservation, so the next `Send` re-prepares against the new settings and history; `Rename` also drops the old entry. A resident service counts as idle while it is `suspended` or `parked`; `SetWorkingDir` is refused for one, because the manifest owns its workspace. They are optional capabilities: each returns an error when the backend does not implement its interface.
- `SetWorkingDir` requires a nonblank existing directory, normalized to an absolute path. It changes only the saved workspace, not files or process cwd; the next runtime rebuilds project instructions and tools against that workspace. Successful changes also clear retained failure diagnostics, so an invalid old workspace can be repaired without starting a runtime.
- `Reply` requires the pending prompt's string ID. `choice` answers the confirmation (`once`/`deny`, plus `session`/`always` when the confirmation carries non-empty `untrusted` names); `text` is accepted but unused. Stale/duplicate replies return `ErrPrompt`. Prompt IDs are unique for the manager lifetime.
- `SetTrustAll` turns a session's auto-approval on or off: while on, every command and file-change confirmation is granted without emitting a prompt, and enabling it also resolves one already pending. The flag is per-session and in-memory — never persisted, cleared on rename/delete — and lives on the manager so it outlives the worker rebuilt for each instruction; `TrustAll` reads it. It needs no backend interface.
- `Subscribe` requires an existing live worker and immediately yields a `snapshot` event. Every event includes a snapshot with `name`, `state`, `queued`, optional `pending` (`id`, `kind`, `title`, optional `tool`, optional `untrusted`, optional `trust_target`), and optional `error`. `state` is one of `starting`, `busy`, `awaiting`, `stopped`; `suspended` and `parked` are reserved for resident services (next phase). A snapshot also carries `context_tokens` (the configured compaction threshold; absent/0 when compaction is off) so a client can show context fill, and `trust_all` (the session's auto-approval toggle). Pending prompts survive disconnect/reconnect. Idle instruction requests have no pending prompt: render input based on `state == "awaiting"` and use `Send`.
- Confirmation prompts include `pending.tool` (and `prompt` event `data.tool`) when associated with a current tool call. This is an independently copied `core.ToolCall`, including exact `Dir`, `Detail`, and `Diff` fields, so reconnecting clients can render the full approval preview without replaying earlier events. Tool fields retain their native Go/JSON names (`Name`, `Target`, `Dir`, `Detail`, `Reason`, `Trusted`, `Diff`, `CommandSegments`, `UntrustedSegments`); `CommandSegments` is the pre-split chain and `UntrustedSegments` indexes the parts needing approval. `pending.untrusted` lists what a client can trust, and `pending.trust_target` says what those names are (`command` or `path`). Questions and unrelated confirmations omit `tool`; completed or consumed tool calls are not reused.
- Events have `type`, optional `data`, and `snapshot`. Types: `snapshot`, `queued`, `busy`, `awaiting`, `prompt`, `prompt_replied`, `prompt_cancelled`, `reason`, `done`, `terminate`, `ask`, `user`, `session`, `reasoning`, `usage`, `tool_call`, `tool_output`, `tool_result`, `notice`, `output`, `separator`, `error`, `stopped`. Observer `ask` is informational; only a confirmation creates a replyable `prompt` (the `ask` action just returns control to the caller's normal input).
- `SubscribeActivities()` returns a global channel of `Activity{Name, Event}`, an idempotent unsubscribe function, and an error. It observes future events for all workers without a browser or per-session subscription, with no replay or initial snapshot. Each of at most 32 global subscriptions buffers 128 events and drops new events when full; it never invokes client callbacks or blocks a worker on output. Payloads and pending prompts are independently copied just as for per-session subscribers. `Close` closes activity channels after final worker events, allowing consumers to drain buffered events. Consumers decide filtering, privacy, formatting, and bounded shutdown waits.
- Each worker supports 32 subscribers with 64 buffered events each. A slow subscriber's channel closes; reconnect for a fresh snapshot and reload history. Unsubscribe is idempotent. There is no retained event log. `core` payloads retain their native field names.
- Finished/evicted worker entries are removed. `Subscribe` then returns `ErrNotFound`; `History` remains available. For failed workers, `Snapshot` retains the final stopped/error state in an in-memory FIFO cache of at most 32 sessions, so startup errors remain visible even if the client missed the events. These diagnostics occupy no runtime slots, disappear on server restart or eviction, and are cleared by an accepted new send or a successful session mutation. Without a retained failure, `Snapshot` returns `ErrNotFound`. `Close` is concurrent-safe, idempotent, cancels workers, and waits for cleanup. Backend/provider implementations must honor their contracts for shutdown to complete.

## Concurrency and workspaces

The concurrency limit bounds live runtimes; idle runtimes can be evicted to
make room. Full busy capacity rejects new work rather than growing an unbounded
queue. Sessions execute in their persisted working directory, defaulting to the
server's launch directory for new sessions. Directories must already exist and
are normalized to absolute paths; they are not security sandboxes, and no request
changes the process cwd. Use separate worktrees when concurrent tasks could
modify the same files.

While serving, PAI exclusively owns the session store across processes. Other
PAI processes cannot open that store until the server stops. Do not delete
`sessions.lock` while PAI is running. Tasks survive browser disconnections,
**not server restarts**; use a service manager such as systemd for hosting.
Shutdown cancels active work and releases store ownership.
