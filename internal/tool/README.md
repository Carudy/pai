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
- `Search` returns Tavily web-search results using the supplied search API key.
- `PaperSearch` searches fixed public arXiv/Crossref APIs without keys, normalizes
  metadata and original abstracts, and shares cancellable provider throttles
  across sessions in one process. Optional contact identifies Crossref requests.
- `PaperSearchRequest.Normalize` validates/defaults the object payload, including
  the `match` mode and pagination `offset`. arXiv keywords are quoted (so a
  model's operators cannot become query syntax), joined all-of or any-of, and a
  strict query that matches nothing retries once as any-of.
- `PaperSearchResult` keeps provider diagnostics alongside records — sent query,
  match mode and fallback, candidate count, offset, year filter, sort, and
  Crossref scores — so a caller can tell a genuine zero from a broken query.
  `Format` bounds UTF-8 output by complete records, preserving identifiers and
  marking unavailable/truncated abstracts and omitted records. It does not
  retrieve full text, infer peer review, or generate AI summaries.

## Boundaries

This package executes operations but never prompts users. Role handlers own
payload validation, confirmation, and conversion of failures to observations.
Do not bypass the shared command parser or duplicate trust logic in adapters.

Suggested validation from the repository root:

```sh
go test ./internal/tool
```
