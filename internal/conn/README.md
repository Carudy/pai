# conn integration contract

## Module overview

`conn` is the terminal's client for a running `pai serve`. It speaks the same
HTTP/SSE API the browser does, maps `runner.Event` values onto the semantic
`core.Observer` vocabulary, and lets the caller render them with the terminal
adapter. It exists because a server owns the session store exclusively, so a
second `pai chat` cannot open it; `conn` never touches the store at all.

See the [module map](../organization.md) for layering.

Suggested validation from the repository root: `go test ./internal/conn`.

## Integration contract

```go
c := conn.New(baseURL, token) // empty baseURL means http://127.0.0.1:9384
err := c.Ping(ctx)            // reachability and credential check

err = conn.Run(ctx, conn.Options{
    Client:   c,
    Session:  "demo",
    In:       os.Stdin,
    Out:      writer,
    Observer: tui.NewLineObserver(writer),
})
```

- `New` only records the base URL and bearer token; it opens no connection and
  reads no config. `Ping` is a `GET /api/sessions` round trip, chosen because it
  requires authentication and touches no session state.
- Mutating requests carry the base URL's origin. The server's mutation gate
  accepts a same-origin request or a bearer token; a terminal has no browser
  origin, so it claims the address it is already talking to. A proxied server
  configured with a different `--public-origin` needs either a token or an
  explicit `Origin` override (`--origin`, set on `Client.Origin`). Plain reads
  carry no `Origin` header.
- Every request except `GET` requires the bearer token (or a same-origin browser
  session). A non-2xx answer is a typed `*StatusError`; a 404 on the event
  stream means "no worker is live for this session", which is a normal state,
  not a failure.
- `Stream` reconnects until its context ends. It polls an idle session about once
  a second so the first instruction attaches promptly, backs off after a dropped
  connection, and stops immediately on `401`/`403`/`400` — retrying a rejected
  credential would only hide the problem. A fatal stream error is surfaced once
  as an `error` event, so the observer can explain it.
- `Run` checks that the session exists (`History`, which also yields the banner's
  role, model, and working directory) before subscribing, then reads
  `/api/snapshot` to decide what to show. A live worker answers the subscription
  at once, and the client renders events from that point on. Stored history is
  replayed when no turn is in flight — no worker at all, or an idle (`awaiting`)
  one with nothing queued or pending — and skipped while a run is busy, because
  the turn being worked on is already stored and printing both would duplicate
  it. A retained failure state is reported so an attach does not look silent.
- Replay renders through the same `Observer` as live events and keeps only what
  a reader needs — instructions, answers, questions, and summaries, with `★`
  marking essential turns. Tool traffic is model-facing and often large, so it is
  left to `/inspect`, which shows the window a request would send.
- Local commands are client-side so they work while a prompt is pending:
  `/help`, `/sessions`, `/inspect [N]`, `/trust [on|off]`, `/model [p:m]`,
  `/role [name]`, `/dir [path]`, `/steer <text>`, `/cancel`, `/quit`. Any other
  `/command` is forwarded to the session (so in-session commands still work), and
  `//text` sends a literal instruction that begins with a slash.
- A pending confirmation consumes the next line: `y`/`once`, `s`/`session`,
  `a`/`always`, anything else denies. Any other prompt (a model question) takes
  the line as its text. Answers go to `Reply`; instructions go to `Send`.
- `Run` returns when input ends or `ctx` is cancelled; it does not cancel the
  remote task. The composition root decides whether `Ctrl+C` should also cancel.
- Multiple clients may attach to one session: each receives the same events, and
  the first reply to a prompt resolves it for everyone.
