# tool

Execution primitives and trust checks used by role tool handlers.
See the [module map](../organization.md) for the execution/approval split.

## Entry points

- `ExecuteCommand` runs a local shell command; `ExecuteCommandAt` sets a
  per-command workspace and streams output through an optional writer.
- `ExecResult` reports command output and status; platform process-group helpers
  support cancellation of local work.
- `SplitSegments` parses a command chain with the real shell parser
  (`mvdan.cc/sh/v3/syntax`) into its separate commands, and is shared with
  `IsTrusted`, so trust decisions and confirmation counts agree. A segment's `Name`
  is empty when running the named command is not the whole story — a construct, a
  file write, a heredoc, or an embedded substitution — so it can never be trusted
  by name. Navigation builtins (`cd`, `pushd`, `popd`) are the exception the other
  way: they run no external code, so they need no grant and are never offered for
  trust.
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

## Trusted commands

Commands matching the `trusted_cmds` list skip confirmation:
```toml
[tool]
trusted_cmds = ["ls", "cat", "grep", "pwd", "which", "df", "ps", "head", "tail"]
```

A chain (`aa && bb`) is confirmed as a whole, but the printed list marks only the
parts that are *not* trusted (`⚠`), and the header counts them (`3 commands (2
need approval):`). The prompt offers more than yes/no:

| Key | Does |
|---|---|
| `y` | run this once |
| `n` | skip |
| `s` | run, and trust the flagged commands for this run |
| `a` | run, and trust them from now on (written to `trusted_cmds`) |
| `Ctrl+C` | abort |

Trust is by command **name** (the first word), so `a` on `sudo rm -rf /x` would
trust `sudo`. To blunt that, `a` is silently downgraded to "this session" for
interpreters and wrappers (`sudo`, `sh`, `bash`, `env`, `xargs`, …). Session
trust is in-memory: it lasts for the run and is not persisted, so a restart
re-confirms.

A step only has a name when running the plain command is the whole story. A loop,
`if`/`case`, a group or subshell, a bare redirection, a file write (`>`, `>>`,
`&>`), a heredoc or here-string, and an embedded `$(…)`/`<(…)` have no name, so
`trusted_cmds` never covers them: they always ask (with no "trust from now on"
option, since there is nothing to scope the grant to). Add `ls` and `ls -la` runs;
`ls > f` and `for …; do ls; done` still ask.

Navigation builtins (`cd`, `pushd`, `popd`) are exempt: they run no external
code, so they are never offered for trust and never prompt on their own.
`cd /tmp/p && ls` asks only about `ls`, while `cd /x && echo y > f` still asks —
the write has no name to trust.

To silence every prompt for a run of work, the browser workspace has a **Trust
all** switch in the composer: while it is on, no confirmation card appears — every
command and file change just runs. It applies to that session only, lasts as long
as the `pai serve` process (it is never written to config), and turning it off
brings the prompts straight back.

## Trusted paths (file tools)

By default every `edit` and `write` asks for approval. List the directories you
work in and changes under them apply straight away — the diff is still shown, so
the change stays visible, but you aren't asked each time:
```toml
[tool]
trusted_paths = ["~/work/myproject"]
```
Paths are resolved (symlinks included) before matching, so a `..` or a symlink
cannot escape a trusted directory. An empty list (the default) confirms
everything; `~` expands, and a relative path resolves against the working
directory. Prefer an absolute path — `["."]` trusts wherever you happen to run
pai, which is rarely what you want.

When a change is outside those paths, the prompt offers the same trust choices as
a command (in the terminal `s`/`a`, in the browser **Trust session**/**Always**):
trust the file's directory for this run, or from now on (added to
`trusted_paths`). Trusting the filesystem root for every session is refused.

This covers the file tools only. Shell commands are still governed by
`trusted_cmds`, so an untrusted `sed -i`/`tee` still prompts even inside a
trusted path.

Reads are not confirmed by default. Set `confirm_read = true` to also require
approval before a file *outside* `trusted_paths` is read — the one place where
content could leave your workspace without a prompt.
