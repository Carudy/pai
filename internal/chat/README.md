# chat

Conversation protocol between the agent loop and an LLM provider.
See the [module map](../organization.md) for the surrounding layers.

## Entry points

- `LoadRolePrompt` / `LoadRolePromptAt` compose role and tool definitions,
  project instructions, and system context; the latter scopes them to a workspace.
- `RolePrompt` keeps a stable prompt head; `OutputGuide` supplies the JSON
  response contract after history on each request.
- `ChatStr` uses `Ports` for provider calls, observation, and logging,
  respecting configured streaming and reporting token usage.
- `ParseResponse` / `ParseResponseWithRetry` validate model actions.
- `Truncate`, `CompactHistory`, and `summarize.go` bound model context;
  shortening replay does not replace durable session history.

## Boundaries

This package handles messages and protocol, not tool execution or user approval.
It depends on semantic `core` ports, never terminal or storage adapters.

Suggested validation from the repository root (not a live provider call):

```sh
go test ./internal/chat
go test -tags filestore ./internal/chat
```
