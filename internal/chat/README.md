# chat

Conversation protocol between the agent loop and an LLM provider.
See the [module map](../organization.md) for the surrounding layers.

## Entry points

- `LoadRolePrompt` / `LoadRolePromptAt` compose role and tool definitions
  (resolved and parsed by `prompts`), project instructions, and the host block
  (from `hostinfo`); the latter scopes them to a workspace.
- `IntroOverride` carries the user's prompt override as a plain value, so the
  override does not widen this package's dependencies. (The settings aggregate
  `config.UserConfig` is still taken directly for streaming, reasoning, and the
  context budget — see the note below.)
- `RolePrompt` keeps a stable prompt head; `OutputGuide` supplies the JSON
  response contract after history on each request.
- `ChatStr` uses `Ports` for provider calls, observation, and logging,
  respecting configured streaming and reporting token usage.
- `ParseResponse` / `ParseResponseWithRetry` validate model actions.
- `Truncate`, `CompactHistory`, and `summarize.go` bound model context;
  shortening replay does not replace durable session history.

## Boundaries

This package handles messages and protocol, not tool execution or user approval.
It depends on semantic `core` ports, never terminal or storage adapters. It also
parses no definition files and probes no host: `prompts` owns the TOML format and
`hostinfo` owns environment probing, so nothing here shells out.

Known coupling: the protocol functions take `config.UserConfig` (and
`ContextConfig`) wholesale, when they use only streaming, reasoning, and the
context budget. Narrowing that to a small options struct would drop the `config`
edge, but it touches every caller, so it is a deliberate open item rather than an
oversight.

Suggested validation from the repository root (not a live provider call):

```sh
go test ./internal/chat
go test -tags filestore ./internal/chat
```
