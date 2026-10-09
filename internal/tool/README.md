# tool

Execution primitives and trust checks used by role tool handlers.
See the [module map](../organization.md) for the execution/approval split.

## Entry points

- `ExecuteCommand` runs a local shell command; `ExecuteCommandAt` sets a
  per-command workspace and streams output through an optional writer.
- `ExecResult` reports command output and status; platform process-group helpers
  support cancellation of local work.
- `SplitSegments` parses quote-aware command chains and is shared with
  `IsTrusted`, so trust decisions and confirmation counts agree.
- `IsTrustedPath` / `IsTrustedPathAt` evaluate configured workspace roots.
- `RemoteManager` manages SSH execution; `NewRemoteManager` configures the
  optional remote-shell wrapper.
- `Search` returns web-search results using the supplied search API key.

## Boundaries

This package executes operations but never prompts users. Role handlers own
payload validation, confirmation, and conversion of failures to observations.
Do not bypass the shared command parser or duplicate trust logic in adapters.

Suggested validation from the repository root:

```sh
go test ./internal/tool
go test -tags filestore ./internal/tool
```
