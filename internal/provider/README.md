# provider

LLM transport and shared request/response types, with no internal dependencies.
See the [module map](../organization.md) for the chat layer above this package.

## Entry points

- `Provider` exposes blocking completion and streamed completion.
- `CreateClient` selects a built-in provider or a generic OpenAI-compatible
  client for a custom name with a configured endpoint.
- `ProviderConfig` describes API key, model, and base URL settings.
- `Message`, `CompletionParams`, `ChatCompletion`, and `ChatCompletionChunk`
  model requests, complete replies, and streaming deltas.
- `ReasoningEffort` and `Usage` carry reasoning controls and token accounting.
- `openai.go` implements HTTP requests and stream decoding, including
  provider-specific request enrichment for built-in providers.

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
