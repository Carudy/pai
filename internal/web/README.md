# Web adapter integration contract

## Module overview

`web` adapts `runner.Manager` to HTTP/SSE and serves the embedded browser UI.
`New` and `Options` are the handler entry points; the composition root owns the
listener, backend, and shutdown. Authentication and origin checks stay in this
adapter, not the agent loop. See the [module map](../organization.md).
Validation commands and the detailed security/API contract remain below.

## Integration contract

```go
handler := web.New(manager, web.Options{Token: token, PublicOrigin: publicOrigin})
server := &http.Server{
    Handler: handler,
    ReadHeaderTimeout: 5 * time.Second,
    IdleTimeout: 60 * time.Second,
    MaxHeaderBytes: 16 << 10,
}
```

Exact exported API:

```go
type Options struct {
    Token string
    PublicOrigin string
    AllowOrigins []string
    ServerCwd string
    Identity string
}
func New(m *runner.Manager, options Options) http.Handler
```

`m` must be non-nil. New does not start a listener, change cwd, access config,
create a manager, or close it. The composition root owns server shutdown and
manager shutdown. Only standard library dependencies are added by this adapter.
`ServerCwd` is the default working directory new sessions offer; `Identity` is
the `user@host` the sidebar displays (`GET /api/info`), supplied by the
composition root because only `hostinfo` probes the host. Both are display
metadata: a client must not treat them as authorization.

**CLI requirements:** reject an empty token for any non-loopback bind (including
wildcard binds). An empty token intentionally disables authentication and is
only suitable for a listener explicitly bound to loopback. Never pass a token
in a URL, log it, or embed it in the page. Remote use requires TLS or a trusted
TLS terminator. `PublicOrigin` optionally specifies the explicit canonical
browser origin, e.g. `https://pai.example` or `https://pai.example:8443`. The CLI
must validate it as an HTTP(S) origin with a host and optional port, no userinfo,
path (including trailing slash), query, or fragment. Empty means derive the
expected origin from `r.Host` and `r.TLS`. Nonempty means compare against that
configured origin exclusively, even if a proxy rewrites the host or terminates
TLS; malformed explicit values fail closed for mutations rather than falling
back to request metadata. `AllowOrigins` optionally lists further already-canonical
origins accepted for mutations, so one server can answer on a second address (a
LAN IP, say) while `PublicOrigin` stays canonical; each is matched on scheme and
host exactly, they widen only the origin gate (another origin still has to
authenticate there, since cookies are per host), and they are not a Host
allowlist. `New` retains its handler-only return type, with no
validation error return or panic. Cookie Secure follows the scheme the browser
actually used — `r.TLS` nonnil, else the accepted request Origin's scheme — so a
plain-HTTP allow-origin is given a non-Secure cookie. A Secure cookie sent over
plain HTTP is dropped by the browser, which would make such a login succeed and
then 401 on the next request. Neither Origin checks nor cookie security trust
`Forwarded` or any `X-Forwarded-*` headers. Host must be validated against the configured public
host/listener by the composition root if DNS rebinding is a concern, especially
in unauthenticated loopback mode. Origin validation is not a Host allowlist.
Each session uses its saved workspace; `/api/working-dir` changes it only while
idle, without changing process cwd. Workspaces are not sandboxes. Run only for
trusted users: authenticated clients can invoke agent tools with the process's rights.

Set header/idle timeouts and a header size limit on the server. The handler sets
10-second body read and 15-second response write deadlines, caps JSON requests
at 64 KiB, session names at 256 bytes, and page sizes at 200. Backend methods
must return promptly (runner's backend API has no request context). SSE uses
5-second per-write deadlines, 10-second heartbeats and a 30-minute connection
lifetime. Do not use a short global server WriteTimeout that breaks SSE. If
middleware wraps ResponseWriter, implement Unwrap so ResponseController can
reach deadline and flush support. Disconnect always unsubscribes, never cancels.
Slow consumers are also bounded/disconnected by runner. No event replay is
promised; reconnect yields a snapshot and the UI reloads durable history. Because
heartbeats are comments (invisible to `EventSource`), the browser cannot tell a
stalled subscription from a quiet run, so the UI re-fetches the snapshot when a
running session has seen no event for ~20s — a slow-consumer eviction or a
buffering proxy then self-heals instead of freezing on the last phase.
Snapshots include `usage` (latest reported request), `total_usage` (sum of
reported request usage), and `usage_calls`. Token fields use core's `Prompt`,
`Completion`, and `Total` names, plus `Cached` — the prompt tokens the provider
served from its prefix cache, 0 when it reports no cache accounting at all, so the
UI omits the figure rather than claiming a miss. `context_tokens`, when present, is the
configured compaction threshold, letting the UI show the latest prompt size
against the budget. The UI hides usage until a report arrives and
renders snapshots rather than adding SSE events, so reconnects cannot double
count. “This run” means the worker runtime, including subsequent instructions;
resuming a retired worker starts fresh, not session-lifetime totals.

Model suggestions come from the configured default, `preset_models`, and saved
sessions, not a provider/model catalog. They render in a dropdown (not a
`<datalist>`, which hides non-matching entries once the field has a value):
focusing the field shows every suggestion, typing filters it, and choosing one
fills the field. The field still accepts any configured `provider:model`, with
spaces around the colon normalized; suggestions are not an allowlist.

The header keeps a compact settings grid (Role, Reasoning, Working dir, and
Model, each with Apply; it collapses to one column on narrow screens, and the
grid itself folds away behind a **Session settings** toggle remembered in
`localStorage`), a Rename
button, and a light/dark theme switch (remembered in
`localStorage`; light is the default, seeded from the system preference on a
first visit). The run status rides in the title row beside the session name at a
fixed height that truncates, so the header does not reflow as the state text
changes. Both status readouts — the phase line and the run clock — sit in one box
whose width is capped (`--status-width`) rather than sized by its text, so a long
`Running execute · …` can neither push the session name nor overlap anything; it
clips. The line leads with one glyph per phase (⏳ while waiting on the model,
🤔 reasoning, 🔧 a tool, 🗜 compacting, 🙋/❓ waiting on the reader), and the spinner
animation is applied only where the machine is the busy one, so a prompt shows a
still glyph. A prompt stops the clock but leaves the box in place, because hiding
it made the status area blink in and out around every confirmation. The clock shows only the elapsed time (`12s (this browser)`) and not the
phase, which the line beside it already names: repeating it said the same thing
twice and doubled the box's length. The phase stays in the clock's tooltip. The
name hugs its text so the Rename button stays next to it, with the slack collecting
in the status box's auto margin. The token line under the settings grid reports
spend only — in and out tokens, this round and for the run. The context figure lives with the context it
describes (see the panel below), because it is a projection rather than a
measurement, and it belongs where compaction is configured. The conversation
loads only the most recent turns and appends older ones as
the reader scrolls to the top (there is no "More history" button); two floating
buttons ride over the timeline — ↑ jumps to the previous user instruction and ↓
to the newest message, each shown only when it would do something. Following the
tail means entering each new card at its heading rather than its end, so a long
answer or tool log is read from where it starts (a short card still lands at the
bottom, because the browser clamps the scroll), and the move is animated — the
follow's own scroll events are skipped so the animation cannot cancel itself, while
an upward move hands control back to the reader. Both panels are bounded: the
activity log keeps the last 60 cards and the in-memory conversation window is
capped (older turns reload from the store) so a long session does not grow
without limit. Confirmation choices render as one aligned row of equal-width
buttons. The new-session form picks the role from a two-column radio grid, so
every role is visible at once instead of hidden behind a dropdown.

Snapshots also recover the current reasoning tail (at most 64 KiB); ordinary
reasoning events carry deltas, not repeated full text. Reasoning is transient
and cleared when the model moves to another action. In the browser it streams
only in the dedicated "Thinking…" preview; when the step ends the finished
block is appended once to the activity log, so the two never show it at once.

CLI and web both respect `[app] streaming` (default `true`). Explicit `false`
uses blocking provider completion, so reasoning appears only after completion.
Enable it with `pai config set streaming true` for incremental reasoning from
providers that send reasoning deltas. SSE flushes each event and sets
`X-Accel-Buffering: no`; proxies must also permit long-lived streaming responses.

## HTTP contract

All responses have CSP, nosniff, no-store and no-referrer headers. Static assets
(page, JS, CSS) are gzip-encoded when the client advertises gzip, with
`Vary: Accept-Encoding`; the sources stay readable in the repository rather than
being committed minified. API failures are JSON `{ "error": "..." }`. Cross-site Sec-Fetch-Site is rejected for all
API requests. Every POST, **including login**, requires an Origin whose scheme
and host match PublicOrigin when set, otherwise the request (no forwarded-header
trust, no CORS). Cookie-authenticated
GETs do not require Origin. `token` and `access_token` query parameters are
rejected; there is no URL authentication.

- `POST /api/login`: `{ "token": "..." }`; constant-time SHA-256 digest
  comparison; issues a fresh random 256-bit, HttpOnly, SameSite=Strict, Path=/
  cookie, Secure when the connection is TLS or the accepted Origin's scheme is
  HTTPS (see the cookie rule above). Credentials expire after 12 hours, are stored only as
  hashes in memory, and are lost on restart. At most 128 unexpired credentials
  per handler. All API routes except login require this cookie if Token is set.
  The static assets (`/`, `/app.js`, `/style.css`) are served unauthenticated; the
  shell ships `hidden` and is revealed only after the first authenticated request
  succeeds, so a signed-out load cannot flash the workspace before the login form
  appears. A 401 shows the login form; any other first-request failure reveals the
  shell so the user still has the UI and an error toast.
- `GET /session/<name>`: the same single-page shell as `/`, so a session URL can
  be bookmarked, shared and reloaded. The app reads the name from the path and
  falls back to the remembered session (with a notice) when it no longer exists.
  Anything else under the prefix, or more than one segment deep, is 404; nothing
  is read from disk by that name, and only `GET` is allowed.
- `GET /api/info`: `{ "identity": "user@host" }`, the server's identity for the
  sidebar. It is display metadata, never authorization, and empty hides the line.
- `GET /api/models`: `{ "models": ["provider:model"], "default_model": "provider:model" }`.
  Discovery uses the configured default and saved session models, not a config
  model list or remote discovery. Only model names are returned, never credentials.
- `POST /api/model`: `{ "name": "...", "model": "provider:model" }`; 200
  `{ "ok": true }`. Requires an idle session; busy/starting, queued work or
  pending prompts return 409. Custom models for configured providers are accepted.
  Saves the model without changing history or the global default; future sends
  use it. Retiring the old runtime closes SSE, so snapshot/events 404 is normal
  until the next send. The browser reloads saved metadata and preserves unsaved
  model, role, and working-directory drafts across history refreshes, but resets
  the corresponding draft on Apply and all drafts on session selection.
- `GET /api/roles`: `{ "roles": ["name"], "default_role": "name" }`; the role
  names the header control offers (built-in plus user roles).
- `POST /api/role`: `{ "name": "...", "role": "..." }`; 202 `{ "ok": true }`.
  Requires an idle session (else 409) and a known role (else 400). Saves the role
  without changing history; the next send rebuilds the prompt and tool set.
- `POST /api/working-dir`: `{ "name": "...", "working_dir": "/path/to/project" }`;
  200 `{ "ok": true }`. Requires an idle session (busy/starting, queued work, or
  pending approvals return 409). The directory must be nonblank, exist on the
  server, and be a directory; relative paths resolve against the server's launch
  directory and are stored as absolute paths. Invalid paths return 400, a missing
  session 404. Saves only the workspace, preserving role, model, and history;
  files are not moved. Retires the runtime and clears retained failure diagnostics,
  so the next send reloads project instructions and scopes tools to the new path.
  No request calls `os.Chdir`; directories are checked again when starting work.
- `POST /api/rename`: `{ "name": "...", "new_name": "..." }`; 200
  `{ "name": "<new>" }`. Requires an idle session; a taken name is 400 and a
  missing session 404. The old runtime is retired and its entry dropped, so the
  browser re-selects the new name.
- `POST /api/delete`: `{ "name": "..." }`; 200 `{ "ok": true }`. Requires an idle
  session (else 409); a missing session is 404. The idle runtime is retired and
  its entry dropped, and the stored history is removed. The browser confirms
  before deleting and, when the open session is deleted, clears the workspace.
- `POST /api/rewind`: `{ "name": "...", "keep": N, "backup": true|false }`;
  202 `{ "ok": true }`. Requires an idle session (else 409); `keep < 0` is 400.
  Keeps the first `keep` turns and drops the rest, retiring the runtime. `backup`
  (omitted = `[session] rewind_backup`) copies the pre-rewind turns into a rolling
  `<name>.rewind-backup` session before truncating; `false` truncates directly.
- `GET /api/sessions?offset=0&limit=100`: `{ "sessions": [runner.Meta], "total": N,
  "default_cwd": "/path" }`. Pagination slices the backend list in its original
  order; List still fetches the full backend list because runner does not expose
  storage pagination. `default_cwd` is the server's launch directory — the
  default for new sessions, which the UI prefills into its working-directory
  field.
- `GET /api/history?name=...&offset=0&limit=100`: `runner.History` (native core
  turn keys `Role`, `Kind`, `Content`, `At`; metadata uses runner's JSON tags).
- `GET /api/context?name=...&offset=0&limit=100`: `runner.ContextView` — the
  message list the session would send: the role's system `head`, one `cards`
  entry per stored turn (`original`, `sent`, and a `state` of `sent` | `elided` |
  `trimmed` | `edited` | `dropped` | `summarized_away` | `capped`), the system `tail`, the
  `model`, and the compaction `config` in effect (`keep_turns`, `max_turns`,
  `elide_after_tokens` — the effective size trigger, already resolved from
  `summarize_after_tokens` when the key is unset — `elide_min_bytes`,
  `elide_head_lines`, `elide_tail_lines`). `estimated_tokens` is the size
  this projection would send — after the same replay-time edits the loop applies
  (over-run cut, elision) — from the same token estimate that seeds the loop's
  summarization budget on resume, so a client can show a context
  size for a session no run has reported usage for yet; it is computed over
  every turn in the replay regardless of the paged window. The estimate counts
  ASCII at four bytes per token and everything else at two, because the old flat
  four-byte rule undercounted CJK and dense code — the text these sessions are made
  of — by roughly a third. Once the session has reported usage it is also
  **calibrated**: `estimate_scale` is `reported / estimated` from the last request
  (clamped to `[0.5, 2]`, absent when uncalibrated), and `estimated_tokens` already
  includes that correction — so the trigger sizes in `config` stay in the provider's
  tokens and remain comparable with the figure. `user_turns` is how many user turns the
  replay holds and `compact_turns` how many a compaction would fold into a
  summary; the latter is 0 when every turn still fits inside `keep_turns`, which
  is what lets the UI explain a manual compact that has nothing to do instead of
  offering a control that silently no-ops. `cards` is a window, not the
  whole transcript: `offset` is the index of its first card and `total` the
  number of stored turns, both normalized, so a client can walk back a page at a
  time (the projection is still computed over every turn, since the replay window
  and elision depend on the full history). It reads persisted state only, so it
  works with or without a live runtime; 404 for an unknown session, 400 for
  invalid pagination. The projection applies the user's overrides, so an edited or
  dropped turn reports what would actually be sent.
- `POST /api/context`: `{ "name": "...", "seq": N, "action":
  "replace"|"drop"|"reset", "content": "..." }`; 200 `{ "ok": true }`. Records
  the user's surgery on one turn — `replace` swaps its text (capped at 32 KiB,
  else 413), `drop` omits it, `reset` clears the override and restores the stored
  text. It changes only what the next send uses, never the stored turn, and
  requires an idle session (else 409). An unknown action or a non-positive `seq`
  is 400; a missing session or turn is 404.
- `GET /api/snapshot?name=...`: `runner.Snapshot`; live state, or a retained
  stopped/error snapshot after a failure (including initialization failures that
  happened before SSE subscribed). The manager retains at most 32 failed-session
  snapshots in memory; an accepted send or successful session mutation clears
  that session's failure. Returns 404 when neither live nor retained state exists.
- `GET /api/events?name=...`: SSE `data: <runner.Event>` frames, default message
  event; heartbeat comments. No Last-Event-ID replay. 404 for no live worker.
- `POST /api/create`: `{ "name": "...", "working_dir": "/path" }`; 201 with
  direct metadata fields `name`, `role`, `model`, `cwd`, `updated_at`. Saves an
  empty session before any message; it starts no runtime and constructs no
  provider.
- `POST /api/send`: `{ "name": "...", "text": "...", "working_dir": "..." }`.
  Omit `working_dir` to use the saved directory; a first send can also create the
  session. A supplied directory must exist, is normalized to an absolute path,
  must match an existing session's saved workspace, and is re-checked when a
  runtime starts. No request changes the process cwd; workspaces are not
  security sandboxes.
- `POST /api/steer`: `{ "name": "...", "text": "..." }`.
- `POST /api/cancel`: `{ "name": "..." }`.
- `POST /api/reply`: `{ "name": "...", "prompt_id": "...", "choice":
  "once"|"deny"|"session"|"always" }`; answers the pending confirmation (an
  omitted choice defaults to deny; a leftover `text` field is accepted but
  unused, since the `ask` action no longer creates a prompt). A confirmation
  whose `pending.untrusted` is non-empty additionally offers trusting those
  command names — or the containing directory when `pending.trust_target` is
  `path` — for the run (`session`) or for good (`always`). A `session` or
  `always` answer also grants the names in the manager, so the grant outlives the
  runtime that asked (see below).
- `GET /api/trust-all?name=...`: `{ "trust_all": bool }`. `POST /api/trust-all`:
  `{ "name": "...", "trust_all": bool }`; 200 `{ "ok": true }`. Trust-all is a
  per-session auto-approval toggle that skips every command and file-change
  confirmation until turned off. It is held in the manager (surviving the worker
  recreated for each instruction), never persisted to config, and reported as
  `trust_all` in the snapshot. A blank/over-length name is 400 and a missing
  session 404. Enabling it also resolves a confirmation already pending.
- `POST /api/trust-cmd`: `{ "name": "...", "cmd": "sudo", "on": bool }`; 200
  `{ "ok": true, "trusted_cmds": [...] }`. The same session grant as a
  confirmation's `session` answer, one command name at a time, for the composer's
  sudo switch: name-based, so `cmd` must be a bare name (a path or a whole command
  line is 400). Held in the manager for the same reason as `trust_all` — the
  runtime is retired by any settings change, while the grant is about the
  conversation — and reported as `trusted_cmds` in the snapshot, which is what the
  switch renders from.

- `GET /api/reasoning?name=...`: `{ "reasoning": "none"|"low"|"medium"|"high",
  "options": [...] }` — the session's in-memory override, else the configured
  `[app] reasoning` default. `POST /api/reasoning`: `{ "name": "...",
  "reasoning": "..." }`; 200 `{ "ok": true }`. Changing it retires the idle
  worker (like the model) and applies to the next run; the override is
  per-session and in memory, never written to config. An unknown effort is 400.

Send/steer/cancel/reply success is 202 (login/model/working-dir/rename/delete 200, create 201), not a promise that the task completed. Stale
prompt IDs are 409, missing live workers 404, full capacity/queues 429, closed
manager 503, malformed/invalid actions 400. JSON must be a single object with
known fields and application/json content type. Pagination defaults to offset
0/limit 100, accepts offsets up to 1,000,000 and limits 1–200.

## Browser UI behavior

Completed assistant replies — the `done` payload and every action's `reason` —
use locally bundled Marked and DOMPurify (see
`vendor/README.md` for pinned versions, provenance, and licenses). Raw HTML is
escaped and Markdown images become alt text before DOM parsing. DOMPurify returns
a DOM fragment with a strict Markdown tag/attribute allowlist; the UI appends it
without assigning unsanitized `innerHTML`. Scripts, images, SVG, MathML, styles,
iframes, and forms are disallowed. Links allow only absolute HTTP(S) URLs and get
`target="_blank"` and `rel="noopener noreferrer"`. Missing/unsupported libraries,
parser errors, and replies over 100,000 characters fall back to literal text.
Other model/history/event content uses DOM textContent. It keeps a
bounded live event pane, renders assistant JSON readably, displays tool-call
Diff previews and reply cards, and replaces history on state refresh rather
than treating transient SSE output as durable turns. Turn cards show their
`turn.At` timestamp, and token counts are compacted to `k`/`M`. The right-hand
**Context** panel logs only context-lifecycle events — finished reasoning blocks
and context notices such as compaction — never tools or run progress, so the
context's evolution stays legible. Switching sessions shows a loading placeholder
in the conversation until the transcript arrives, rather than an empty pane.
A chained command in a card is numbered at its operators (as the CLI prints it),
so the piece that failed is identifiable at a glance. A `read` card carries its
line range on the path (`main.go 84:97`) instead of a separate labelled row. That
panel's header carries the context figure and a **Compress now** button. The
figure is the projection's estimated prompt size measured against the
`summarize_after_tokens` budget and marked `estimated`, never the provider's own
count: the projection is recomputed from the stored turns, so it moves the moment
a compaction or a card edit lands, whereas a reported prompt size only changes on
the next request — and right after a manual compress it still counts the span that
was just summarized. The provider's own numbers, and the last request's prompt
size in the tooltip, are shown as spend in the header instead.

**Compress now** forces the compaction that `summarize_after_tokens` would
trigger later: it sends the CLI's `/compact` as an instruction (a command is
dispatched wherever an instruction is read), behind a confirmation, because it
costs one model call. It is offered only for an idle session that has something
to compress, and the outcome — `compacted N messages into a summary (M kept
verbatim)`, or `nothing to compact yet` — lands in this same panel, since command
output is a context event. The `M kept verbatim` turns are the last `keep_turns`
user turns, which the summary deliberately does not cover: they stay in the
replay, so they keep showing as `sent` while everything before the checkpoint
shows as `summarized away`. Compaction counts **whole user turns**, so a session
can be long in messages and still have nothing older than the `keep_turns` window;
the projection reports that up front (`compact_turns`), the button's tooltip says
it, and a press that would find nothing explains why rather than looking dead.
While the summarizer runs the snapshot's phase is `compacting`, which the header
shows as `Compressing history…` — the call does not stream, so without it the UI
would sit on its previous phase for the whole call. The projection is probed when a session is opened, so
the figure is there before any run has reported usage. An **Inspect** switch in that panel's header
opens an overlay of the messages a send would use — the system head, one card per
message showing the original and what is actually sent (with a state badge for
elided, over-run, edited, dropped, summarized-away and capped messages), and the tail, with
the compaction settings that produced them. The overlay is windowed: it opens
anchored on the newest page, names the slice it holds, offers the page above, and
loads it when the reader clicks or scrolls to the top, so a long transcript is
never fetched whole and the reader's place is held across a prepend. Cards are
raw text — never Markdown — because the point is to see exactly the bytes a send
would use. Messages are grouped by user turn — the unit compaction and
`max_turns` count — and the floating jump
arrows are hidden while the overlay is open. Cards that are in the replay window
offer per-message **Edit…** (an inline textarea seeded from the sent text),
**Drop**, and — when overridden — **Reset**; an edited card hides its stored
original behind a detail, and a card outside the window (summarized-away or
capped) shows why it cannot be edited rather than a no-op control. The controls
are disabled while the session runs. It is reconstructed from the store rather
than captured live, so it shows what compaction and the overrides actually did
rather than a guess; applying surgery retires the idle runtime, so the next send
uses it. A Reasoning dropdown sets the session's
effort (in memory; applies to the next run). Snapshots recover pending
prompt titles/IDs and retained `pending.tool` approval context (native core
keys `Name`, `Target`, `Dir`, `Detail`, `Reason`, `Diff`) on reconnect, page reload, and
session switch, together with `pending.untrusted` (what a confirmation can
trust) and `pending.trust_target` (`command` or `path`, so the card words it
correctly). Approval cards render only this retained tool, not a transient
last tool event. The floating prompt card can be dragged aside by its title
(clamped to the conversation panel; its position resets when the prompt changes), and
the reasoning card is draggable too, remembering its position across reasoning
sessions (it is only visible while reasoning). Repeated snapshots for the same session/pending ID preserve
the existing question form, focus, and draft; a new ID or cleared prompt resets
the card. Drafts are not persisted across page reloads or session switches. Selecting
another session closes its subscription but never calls Cancel. New named
sessions start on their first Send, not on selection. The workspace remembers the
open session in `localStorage` (never a cookie: it is only needed after the page
loads, so it need not travel on every request and needs no server state) and
reopens it on reload if it still exists. The open session is also named in the
URL (`/session/<name>`), which wins over the remembered name; back and forward
move between sessions, and a link to a session that has gone is reported and
dropped. Transient `notice` events (a trust
downgrade, a newly trusted directory) surface as short-lived toasts above the
composer in addition to the activity log.

The composer is three columns: the per-session trust switches on the left —
**Trust all** (while on, no confirmation card appears at all) and **Allow sudo**
(the same session grant for the `sudo` name alone, so a whole task does not stop
at every sudo; it grants the name, so sudo still has to run, and PAI still asks
about every other command). Both carry their warning in the tooltip rather than a
visible hint, which is what makes room for two of them; both render their state
from the snapshot (`trust_all`, `trusted_cmds`), never from what this browser
asked for). Then the instruction box in the middle, and a 2×2 action pad on the
right — Send and **Apply plan** (which sends the canned instruction `Apply your
plan.` without touching the draft), then Steer and Cancel. Both switches are
disabled until a session is selected, and the server is the authority for their
state. Narrow screens stack the columns and keep the button grid.

## Scripts and validation

Load `/vendor/marked.min.js`, then `/vendor/purify.min.js`, then `/app.js` using
local script tags (ordered scripts, not async). No Node or CDN is needed at
runtime or during Go builds. The existing `markdown` CSS class is preserved.

Optional actual sanitizer tests: install a current compatible jsdom in a temporary
directory outside the repository, then run
`PAI_WEB_JSDOM=/absolute/path/to/node_modules/jsdom node internal/web/app_test.js`.
Without that variable the dependency-free harness tests URL policy, renderer
wiring, literal fallbacks, and the async flows (context paging, model/role/workspace
apply, rewind, session list), and explicitly skips actual DOM sanitization tests.
It does not claim its minimal fake DOM validates DOMPurify. The harness's async
suites hang forever — silently swallowing their own assertion failures — if a
stubbed `api` resolver is stored on a shared global while another call is in
flight, so stub it per path.

Validation: `go test ./internal/web`.
Security/action tests use httptest and a local fake provider, never real API
keys, config, storage, or external network. `TestPendingPromptUI` runs the
no-dependency DOM regression harness when Node is available; otherwise it
explicitly skips. Run it independently with `node internal/web/app_test.js`.
