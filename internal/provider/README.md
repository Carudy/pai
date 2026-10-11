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
  including provider-specific request enrichment for built-in providers.
- `responses.go` implements OpenAI's Responses API (`POST /responses`), whose
  named SSE events and reasoning-summary shape are flattened into the same
  `ChatCompletion`/`ChatCompletionChunk` types. Requests are stateless
  (`store = false`).
- `http.go` holds the shared JSON-POST plumbing both transports use.

## Boundaries

This package transports messages; it does not load user TOML, compose prompts,
validate agent actions, or render output. `config` holds user provider settings
and callers pass credentials to the transport; never include keys in docs/logs.
Custom `base_url` values are complete chat-completion endpoint URLs.

Suggested validation from the repository root:

```sh
go test ./internal/provider
go test -tags filestore ./internal/provider
```
