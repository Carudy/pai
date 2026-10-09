# core

Shared semantic ports and value types used by the loop and its adapters.
See the [module map](../organization.md) for the ports-and-adapters architecture.

## Entry points

- `Observer` receives agent, tool, reasoning, and usage events.
- `Prompter` asks questions and requests confirmation; optional `Steerer`
  supplies instructions at safe points while work is in progress.
- `Logger` carries diagnostics, and `Recorder` appends conversation turns.
- `Sessions` exposes storage actions needed by in-session commands.
- `Turn`, `ToolCall`, `ToolResult`, and `Usage` are shared values;
  `ErrAborted` distinguishes prompt cancellation from failure.
- `WriterFunc` adapts streamed tool output; `Version` supports release injection.

## Boundaries

Keep this package dependency-light, with no internal package dependencies.
Ports carry meaning, not ANSI styling, HTTP details, or storage implementations.
Adapters implement the interfaces and the composition root connects them.

Suggested interface inspection from the repository root (no package tests):

```sh
go doc ./internal/core
```
