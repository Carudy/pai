# session

Durable conversation storage behind a backend-independent interface.
See the [module map](../organization.md) for the storage adapter boundary.

## Entry points

- `Open` opens the pure-Go SQLite store (`modernc.org/sqlite`, so builds stay
  cgo-free). `Backend` names the engine for display.
- `Store` creates, loads, lists, appends, renames, and deletes sessions.
- `Meta` includes role, model, cwd, title, timestamps, and turn count;
  `Session` pairs metadata with ordered `core.Turn` history. `Meta.ID` is the
  immutable identity used by events, watchers, mail, and tasks; `Name` stays the
  display key and is what turns and the CLI reference. `Meta.Mode` (chat or
  service), `Meta.State`, and `Meta.NextWake` carry resident-session state.
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
- `Store.SetEssential` marks or unmarks one stored turn. It is a property of the
  turn (`turns.essential`, added by an additive migration), not an override:
  replay reads it to skip elision and the summarizer is told to preserve that
  content, and the flag is revocable. `Get` returns it on the turn, so the
  inspector and the chat cards can show and toggle it.
- `NewRecorder` implements `core.Recorder`; `controller.go` adapts session
  operations to the loop's storage port.

## Service state

- `ServiceStore` is implemented by the SQLite store (type-assert a `Store`). It
  persists a service's status document, manifest spec, next wake, a durable
  `core.Event` queue, and its watchers. Events use states
  `pending/leased/done/dead`; a lease left by a crash is reset to pending on the
  next open, and a dedupe key replaces an existing pending event rather than
  stacking duplicates. Delivered (`done`) events older than 30 days are pruned
  when the store opens and after an ack; dead-lettered events stay visible for
  inspection. Events are ordered by `priority` descending (bigger is
  more urgent), then deadline and arrival; `DueEvents` only returns work whose
  retry time has arrived, while `QueuedEvents` also lists leased and dead work
  for inspection. `NextEventAt` reports the earliest pending retry so a nacked event
  can schedule the next wake instead of leaving the session idle. Watchers are
  keyed by `(session_id, name)` and keep their last observed state, next run, and
  last error. A user/mail turn records its originating event in
  `turns.event_id`; `HasTurnEvent` lets a restarted service skip re-recording an
  event whose turn was already written.
- State changes (`SetServiceState`, `SetNextWake`, `SetUsage`) deliberately leave
  `updated_at` alone: a timer probe must not reorder the session list every cycle.
- `ServiceInfo` adds the status document and manifest spec to `Meta`, so listing
  chat sessions does not load either.

## Groups

- `GroupStore` (also type-asserted on a `Store`) is the group directory:
  `groups` (name, shared goal, optional lead, member cap) and `group_members`,
  which references the immutable session id so a rename cannot move a session
  and deleting one drops its membership through the foreign key. A session is in
  at most one group, and a lead, when set, must be a member.
- `GroupFromNames` turns a definition written by a person (member names, an
  optional lead) into the ids the store needs, and is the only place the "a member
  is a resident service" rule is stated. The CLI manifest and the web form both
  call it, so the two surfaces cannot drift.
- Membership reads are single indexed lookups, so nothing is scanned or
  backfilled at startup.
- A group carries no scheduler state: it owns no wake counter and cannot pause a
  member. The only durable facts are the roster, the shared goal, and an optional
  lead. `groups.lead_id` is deliberately not a foreign key: a lead that no longer
  exists reads as "no lead" (`GroupByName` left-joins the name), and both delete
  paths refuse to create one in the first place.
- `group_members.intro` is the operator's one-line description of what a member
  does for this group. It hangs off the membership, not the session, and is
  written by `SetGroupMemberIntro` (empty clears it); `ApplyGroupIntros` writes
  the set a manifest names, after the group exists.
- `TaskStore` is the group board: `tasks` rows with a status, an owner, a lease,
  and a version. A claim is a lease, so a lapsed one is recoverable without an
  operator; every write is optimistic, so a stale edit is rejected with the
  current version instead of merged. `DueEscalations` and `MarkTaskEscalated`
  implement the host's exactly-once notification for stuck work. Deleting the
  group cascades to its board; deleting a session leaves its tasks ownerless so
  the work is not lost.
- `ActivityStore` is the group timeline's raw material, and it is two reads
  because the two sides are keyed differently: `GroupEvents` returns the host's
  deliveries to a set of session ids (mail, broadcast, user input, escalation —
  probes are a member's own business and are excluded), and `RecentOutputs`
  returns the most recent assistant outputs for a set of display names. The
  store does not interpret a report's content; the adapter parses the action out
  of it.
- Migrations are additive: existing databases gain `id`, `mode`, `state`,
  `status`, `spec`, and `next_wake`, rows are backfilled with an immutable id,
  and `PRAGMA user_version` is set. v2 adds `events.priority` and backfills it
  from the class bands; `events.trace`/`events.hops` and the group tables are
  created if missing.

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
