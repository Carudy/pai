# provider

LLM transport and shared request/response types, with no internal dependencies.
See the [module map](../organization.md) for the chat layer above this package.

## Entry points

- `Provider` exposes blocking completion and streamed completion.
- `CreateClient` selects a transport from a `ProviderConfig`: the OpenAI-compatible
  chat completions API by default, or the Responses API when
  `Protocol = ProtocolResponses`. An unknown protocol is rejected here.
- `ProviderConfig` describes the provider name, API key, model, base URL,
  protocol, and any extra HTTP `Headers`. On creation PAI resolves the request
  headers: it always sends a `User-Agent`, and for the OpenCode Go gateway
  (`opencode.ai`) it generates the `x-opencode-session` routing id that gateway
  requires. A user-supplied header of the same name always wins.
- `Message`, `CompletionParams`, `ChatCompletion`, and `ChatCompletionChunk`
  model requests, complete replies, and streaming deltas.
- `ReasoningEffort` and `Usage` carry reasoning controls and token accounting.
- `openai.go` implements the chat completions transport (HTTP + `data:` SSE),
  including provider-specific request enrichment for built-in providers. Streamed
  requests opt into the final usage chunk (`stream_options.include_usage`) so token
  accounting — and the `summarize_after_tokens` budget it drives — survives streaming.
- `responses.go` implements OpenAI's Responses API (`POST /responses`), whose
  named SSE events and reasoning-summary shape are flattened into the same
  `ChatCompletion`/`ChatCompletionChunk` types. Requests are stateless
  (`store = false`).
- `http.go` holds the shared JSON-POST plumbing both transports use.

## Timeouts and retries

Every call is bounded. Waiting forever is not a state the CLI can explain or
escape without Ctrl+C, so:

- **First byte** (streamed calls): 2 minutes for response headers (connect,
  queue, prompt prefill). Non-streaming calls have no header deadline of their
  own — the server sends headers only when the body is ready — and are bounded by
  the outer limit below.
- **Stream idle**: 90 seconds of silence inside a stream closes the body, which
  unblocks the read; the caller sees `ErrStalled` instead of a hang. A provider
  that streams reasoning sends deltas throughout, so a gap this long is a stall.
- **Non-streaming**: 10 minutes for the whole call, since the body only arrives at
  the end and only an outer bound can stop a dead one.

Transient failures are retried **before any response data is delivered**: 429,
5xx and connection errors get up to three attempts with exponential backoff
(0.5 s, 1 s), and a timeout gets two attempts because each already waited two
minutes. `Retry-After` is honoured, but a pause longer than 15 seconds is reported
rather than sat through. A 4xx other than 429 is never retried — the request
itself was rejected.

A stream that stalls *after* delivering data is reported, never replayed: the
reader already saw part of the answer, and resending would duplicate it on
screen. `chat` retries that case once only when nothing had been rendered yet.

## Boundaries

This package transports messages; it does not load user TOML, compose prompts,
validate agent actions, or render output. `config` holds user provider settings
and callers pass credentials to the transport; never include keys in docs/logs.
Custom `base_url` values are complete chat-completion endpoint URLs.

Suggested validation from the repository root:

```sh
go test ./internal/provider
```
