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
}
func New(m *runner.Manager, options Options) http.Handler
```

`m` must be non-nil. New does not start a listener, change cwd, access config,
create a manager, or close it. The composition root owns server shutdown and
manager shutdown. Only standard library dependencies are added by this adapter.

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
back to request metadata. `New` retains its handler-only return type, with no
validation error return or panic. Cookie Secure is true when `r.TLS` is nonnil
**or** PublicOrigin uses HTTPS. Neither Origin checks nor cookie security trust
`Forwarded` or any `X-Forwarded-*` headers. Host must be validated against the configured public
host/listener by the composition root if DNS rebinding is a concern, especially
in unauthenticated loopback mode. Origin validation is not a Host allowlist.
The backend fixes the workspace; no API can change cwd. Run only for trusted
users: authenticated clients can invoke agent tools with the process's rights.

Set header/idle timeouts and a header size limit on the server. The handler sets
10-second body read and 15-second response write deadlines, caps JSON requests
at 64 KiB, session names at 256 bytes, and page sizes at 200. Backend methods
must return promptly (runner's backend API has no request context). SSE uses
5-second per-write deadlines, 10-second heartbeats and a 30-minute connection
lifetime. Do not use a short global server WriteTimeout that breaks SSE. If
middleware wraps ResponseWriter, implement Unwrap so ResponseController can
reach deadline and flush support. Disconnect always unsubscribes, never cancels.
Slow consumers are also bounded/disconnected by runner. No event replay is
promised; reconnect yields a snapshot and the UI reloads durable history.
Snapshots include `usage` (latest reported request), `total_usage` (sum of
reported request usage), and `usage_calls`. Token fields use core's `Prompt`,
`Completion`, and `Total` names. The UI hides usage until a report arrives and
renders snapshots rather than adding SSE events, so reconnects cannot double
count. “This run” means the worker runtime, including subsequent instructions;
resuming a retired worker starts fresh, not session-lifetime totals.

Model suggestions come from the configured default and saved sessions, not a
provider/model catalog. The input accepts any configured `provider:model`, with
spaces around the colon normalized; suggestions are not an allowlist.

Snapshots also recover the current reasoning tail (at most 64 KiB); ordinary
reasoning events carry deltas, not repeated full text. Reasoning is transient
and cleared when the model moves to another action.

CLI and web both respect `[app] streaming` (default `true`). Explicit `false`
uses blocking provider completion, so reasoning appears only after completion.
Enable it with `pai config set streaming true` for incremental reasoning from
providers that send reasoning deltas. SSE flushes each event and sets
`X-Accel-Buffering: no`; proxies must also permit long-lived streaming responses.

## HTTP contract

All responses have CSP, nosniff, no-store and no-referrer headers. API failures
are JSON `{ "error": "..." }`. Cross-site Sec-Fetch-Site is rejected for all
API requests. Every POST, **including login**, requires an Origin whose scheme
and host match PublicOrigin when set, otherwise the request (no forwarded-header
trust, no CORS). Cookie-authenticated
GETs do not require Origin. `token` and `access_token` query parameters are
rejected; there is no URL authentication.

- `POST /api/login`: `{ "token": "..." }`; constant-time SHA-256 digest
  comparison; issues a fresh random 256-bit, HttpOnly, SameSite=Strict, Path=/
  cookie, Secure on TLS or an HTTPS PublicOrigin. Credentials expire after 12 hours, are stored only as
  hashes in memory, and are lost on restart. At most 128 unexpired credentials
  per handler. All API routes except login require this cookie if Token is set.
- `GET /api/models`: `{ "models": ["provider:model"], "default_model": "provider:model" }`.
  Discovery uses the configured default and saved session models, not a config
  model list or remote discovery. Only model names are returned, never credentials.
- `POST /api/model`: `{ "name": "...", "model": "provider:model" }`; 200
  `{ "ok": true }`. Requires an idle session; busy/starting, queued work or
  pending prompts return 409. Custom models for configured providers are accepted.
  Saves the model without changing history or the global default; future sends
  use it. Retiring the old runtime closes SSE, so snapshot/events 404 is normal
  until the next send. The browser reloads saved metadata and preserves unsaved
  model drafts across history refreshes, but resets them on selection or Apply.
- `GET /api/sessions?offset=0&limit=100`: `{ "sessions": [runner.Meta], "total": N }`.
  Pagination slices the backend list in its original order; List still fetches
  the full backend list because runner does not expose storage pagination.
- `GET /api/history?name=...&offset=0&limit=100`: `runner.History` (native core
  turn keys `Role`, `Kind`, `Content`, `At`; metadata uses runner's JSON tags).
- `GET /api/snapshot?name=...`: `runner.Snapshot`; 404 for no live worker.
- `GET /api/events?name=...`: SSE `data: <runner.Event>` frames, default message
  event; heartbeat comments. No Last-Event-ID replay. 404 for no live worker.
- `POST /api/send`, `/api/steer`: `{ "name": "...", "text": "..." }`.
- `POST /api/cancel`: `{ "name": "..." }`.
- `POST /api/reply`: `{ "name": "...", "prompt_id": "...", "text": "...",
  "approve": false }`; text answers questions, approve answers confirmations.

Send/steer/cancel/reply success is 202 (login/model 200), not a promise that the task completed. Stale
prompt IDs are 409, missing live workers 404, full capacity/queues 429, closed
manager 503, malformed/invalid actions 400. JSON must be a single object with
known fields and application/json content type. Pagination defaults to offset
0/limit 100, accepts offsets up to 1,000,000 and limits 1–200.

Completed assistant replies use locally bundled Marked and DOMPurify (see
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
than treating transient SSE output as durable turns. Snapshots recover pending
prompt titles/IDs and retained `pending.tool` approval context (native core
keys `Name`, `Target`, `Detail`, `Reason`, `Diff`) on reconnect, page reload, and
session switch. Approval cards render only this retained tool, not a transient
last tool event. Repeated snapshots for the same session/pending ID preserve
the existing question form, focus, and draft; a new ID or cleared prompt resets
the card. Drafts are not persisted across page reloads or session switches. Selecting
another session closes its subscription but never calls Cancel. New named
sessions start on their first Send, not on selection.

Load `/vendor/marked.min.js`, then `/vendor/purify.min.js`, then `/app.js` using
local script tags (ordered scripts, not async). No Node or CDN is needed at
runtime or during Go builds. The existing `markdown` CSS class is preserved.

Optional actual sanitizer tests: install a current compatible jsdom in a temporary
directory outside the repository, then run
`PAI_WEB_JSDOM=/absolute/path/to/node_modules/jsdom node internal/web/app_test.js`.
Without that variable the dependency-free harness tests URL policy, renderer
wiring and literal fallbacks, and explicitly skips actual DOM sanitization tests.
It does not claim its minimal fake DOM validates DOMPurify.

Validation: `go test ./internal/web` and `go test -tags filestore ./internal/web`.
Security/action tests use httptest and a local fake provider, never real API
keys, config, storage, or external network. `TestPendingPromptUI` runs the
no-dependency DOM regression harness when Node is available; otherwise it
explicitly skips. Run it independently with `node internal/web/app_test.js`.
