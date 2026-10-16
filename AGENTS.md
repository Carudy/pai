# AGENTS.md — notes for coding agents

Working notes for **PAI**. Read [`internal/organization.md`](internal/organization.md) for the module map.

## Mental model

- **One loop, data-defined roles.** `internal/role` is the only agent loop; a
  role is an intro + a tool list in `internal/prompts/roles/<name>.toml`, so
  adding one is a file, not Go code. Users append to a role's intro (never
  replace) via `~/.config/pai/role-extra.toml`; there is no separate "skills"
  feature — an on-demand guide is just a role-intro pointer to a file the model
  `read`s.
- **`cli` is the composition root.** Only `cli` imports `tui` and `session`; the
  loop talks to `core` ports and adapters implement them.
- **The model speaks JSON.** Every response is one object with `action`
  (`tool` | `ask` | `done` | `terminate`), `payload`, and `reason`; a `tool`
  action also names `toolname` at the top level. `ask` is non-blocking — the
  question is recorded like a reply and the answer arrives as the next
  instruction, so only tool confirmations create a replyable prompt.
  `chat.OutputGuide()` is re-rendered after the history each turn so the format
  can't be forgotten.

## Commands & environment

```bash
make build        # build the stripped binary
make test         # run the test suite
make vet          # run go vet
make fmt          # gofmt
make deps         # pre-fill the module cache
make install      # go install ./cmd/pai
```

Machine-specific gotchas that waste time if ignored:

- **The module proxy is blackholed**, so `go build`/`vet`/`test` can appear to
  hang ~30s. Pre-fill the cache once with `make deps`, or:

  ```bash
  HTTPS_PROXY=http://127.0.0.1:7890 HTTP_PROXY=http://127.0.0.1:7890 go mod download
  ```

  Use plain `go mod download` (no `all`) — `all` widens `go.sum` beyond what
  `go mod tidy` keeps, producing a spurious diff.
- **Browser JavaScript:** run the harness with `node internal/web/app_test.js` (Node
  is on PATH). `go test ./internal/web` also runs it via `TestPendingPromptUI`, so
  a JS regression fails the Go suite. The real-DOM sanitizer cases still need
  `PAI_WEB_JSDOM=/path/to/jsdom`.
- **`timeout` is not on PATH on macOS** (use `gtimeout`, or a bounded tool
  timeout); its absence once silently made a command look like it succeeded.

## Validation — fast feedback first

Pick checks by what changed; don't run the full matrix after every edit. When
delegating, subagents run scoped checks and the parent runs integration checks
once after merging, rather than repeating suites.

| Change | Check |
|---|---|
| Docs/comments only | Inspect links/content and `git diff --check`; no build/test |
| CSS/layout/copy | `go test ./internal/web -run 'TestPage\|TestHTML'` plus a browser check if available |
| Browser JavaScript | `node internal/web/app_test.js`, plus `internal/web` tests (which run the same harness); report any real-DOM/jsdom cases left unverified |
| Isolated Go implementation | `gofmt` the changed files, then targeted package tests (`-run` first, broaden as needed) |
| Shared interfaces, runtime lifecycle, cross-package wiring | Tests for affected packages and direct consumers; build once at the integration checkpoint |
| Storage/platform code | Test affected packages; cross-compile affected targets if platform behavior changed |
| Concurrency/cancellation/events | Targeted `go test -race` for affected packages, not the whole repo |

Run the full matrix **once before finalizing a substantial feature, release, or
broad refactor** (or when asked):

```bash
go build ./...
make vet
make test
```

Don't repeat an unchanged check after a later small edit — Go tests compile
their packages, so a separate build adds little for isolated tests. Always state
what was checked and what remains unverified.

### Live smoke test

Useful for prompt/protocol changes, not required for small fixes, styling, or
docs. Run it only with an isolated environment and approved credentials, and
never let it touch your real config:

```bash
mkdir -p /tmp/paihome/.config/pai
printf 'interactive = false\n' > /tmp/paihome/.config/pai/config.toml
go build -o /tmp/pai ./cmd/pai
HOME=/tmp/paihome /tmp/pai "list the current dir"
```

- **Set `interactive = false`** in the temp config; otherwise every run *blocks
  at an input prompt* — correct behaviour that looks like a hang in automation.
- **Don't run `go` with a temp `HOME`** in the same command: Go will re-download
  the world. Build first, then run the binary with the temp `HOME` (or pin
  `GOMODCACHE`/`GOPATH`/`GOCACHE`).
- Clean up `/tmp/paihome` and stray `pai` processes afterwards.

## Where to change what

| Task | Touch |
|---|---|
| New **role** | add `internal/prompts/roles/<name>.toml` (no Go); `context_files` folds repo instruction files (e.g. `AGENTS.md`) into the system prompt |
| New **tool** | implementation in `internal/role/tools.go` **and** `internal/prompts/tools/<name>.toml`; `checkToolCoverage` enforces both exist. `internal/tool` only *executes* — the handler confirms with the user via `rt.Prompter` |
| New **config key** | `internal/config/types.go` (struct + default), the `configKeys` table in `internal/cli/config.go`, and `internal/config/template.go`. An optional key belongs in the template commented out; merge adds it as a comment, never an active value |
| New **subcommand** | add to the table in `internal/cli/command.go`; put handlers in a sibling file (`config.go`, `role.go`, …) |
| New **in-session `/command`** | add to `commandSpecs` in `internal/role/command.go` — arity and an optional `check` hook are declared there, and `/help` is generated from it. Storage-touching commands use the `core.Sessions` port |
| New **session backend** | implement `session.Store` and select it in `session.Open` |

## Style to preserve

- **Comments explain *why*, not *what*.** Document intent, invariants, and
  tradeoffs (e.g. "rendered after the history so it's the last thing the model
  reads"); don't narrate obvious code.
- **Small, single-purpose packages.** Keep the layering in
  `internal/organization.md` intact: never import `tui` or `session` from
  `chat`/`role`/`tool`, and keep the bottom seams one-per-concern — only
  `prompts` parses role/tool definitions and only `hostinfo` probes the host, so
  `chat` imports no TOML decoder and shells out to nothing.
- **Ports carry meaning, not presentation.** Don't put ANSI/lipgloss concerns in
  `core`; the adapter decides rendering.
- **Validate at the boundary; feed failures back as data.** A tool handler does
  not error on bad model output — it returns a `[tool error]` observation so the
  model can self-correct.
- **Never hardcode role or tool lists.** Generate them from `prompts.RoleNames()`
  / `prompts.ToolNames()` (help, validation, errors); a stale hardcoded list was
  a real past bug.
- **Wrap errors with context** (`fmt.Errorf("...: %w", err)`), keep messages
  lowercase and specific.

## Pitfalls

- **Tool payload shapes must match the handlers.** `websearch` takes a
  **string** payload; `execute` takes a **string** (the command) or an **object**;
  the rest take an **object**:

  ```
  execute      "df -h"
               or {"cmd": "df -h", "dir": "/var/log"}
  papersearch  {"source":..., "query":..., "limit":..., "year_from":..., "year_to":..., "sort":..., "match":..., "offset":...}
  remote       {"host":..., "cmd":...}
  read         {"path":..., "offset":..., "limit":...}
  edit         {"path":..., "old_string":..., "new_string":..., "replace_all":...}
               or {"path":..., "edits":[{"old_string":..., "new_string":...}]}
  write        {"path":..., "content":...}
  ```

  Getting one wrong yields `json: cannot unmarshal object into Go value of type
  string` (or a missing-field error) at runtime.
- **File edits are planned before approval.** Batch entries match the original
  snapshot, not sequential output; reject overlaps and stale approvals. Keep
  complete-line read budgets and atomic no-clobber creation. See
  [`internal/role/filetools.md`](internal/role/filetools.md) for limits and the
  best-effort external-writer protection.
- **`role.Runtime` is per-run state**, not configuration (that's
  `config.UserConfig`) — don't mix them. `Session` is reserved for persistence
  (`session`); the loop's state is `Runtime`.
- **Command chaining has one parser.** `tool.SplitSegments` uses the real shell
  parser (`mvdan.cc/sh/v3/syntax`) and is the single source of truth for "how
  many commands is this": `IsTrusted` and the confirmation display both use it,
  so the count shown can't disagree with what trust saw. A quoted operator is
  data (`grep 'a|b' f` is one command), a bare `&` separates (backgrounding runs
  what follows), a heredoc body belongs to its command rather than becoming
  segments named `line`/`EOF`, and a compound construct (loop, conditional,
  group) is one unit with no command name.
- **Trust is name-based, and only a plain invocation has a name.** A segment's
  `Name` is its `argv[0]`, empty when the named command isn't the whole story — a
  construct, a bare redirection, a file write (`>`, `>>`, `&>`), a heredoc or
  here-string, or an embedded `$(...)`/`<(...)`. A nameless segment is never
  covered by `trusted_cmds`, so it always prompts (with no trust option, since
  there's nothing to scope a grant to). Add `ls` to the trusted list and `ls -la`
  runs unprompted; `ls > f`, `ls $(x)`, and `for …; do ls; done` do not.
  Navigation builtins (`cd`, `pushd`, `popd`) are exempt — they run no external
  code, so they need no grant and are never offered for trust — which is why
  `cd /tmp && ls` asks only about `ls` while `cd /x && echo y > f` still asks.

## Dependency & binary policy

- **Pure Go, cgo-free.** Every release target cross-compiles with
  `CGO_ENABLED=0`, so no C toolchain or per-target runner is needed. SQLite uses
  `modernc.org/sqlite`, the session lock uses `golang.org/x/sys`, and command
  parsing uses `mvdan.cc/sh/v3/syntax` — all three are pure Go. A stripped binary
  stays ~13 MB.
- **Prefer existing dependencies.** Before adding one, prefer what's already used
  (`BurntSushi/toml`, the Charm stack) or the standard library. New dependencies
  need a clear justification against the size/complexity budget; the shell parser
  earns its ~330 KB by replacing a hand-rolled splitter that mis-read heredocs
  and compound commands.
- **Subcommands over flag sprawl**, with short aliases and generated help.

## Releasing

Pushing a `v*` tag is the whole release: `.github/workflows/release.yml` runs the
tests, cross-compiles the static binaries (linux/darwin/windows), and publishes a
GitHub Release with `--generate-notes` and `checksums.txt`.
`.github/workflows/ci.yml` runs gofmt/vet/test on `main` and PRs.

`core.Version` is a `var` so the release workflow can inject the tag with
`-ldflags "-X github.com/Carudy/pai/internal/core.Version=<tag>"`. The value in
source is only the fallback for local builds, so a release need not bump it — but
keep it near the latest tag so those builds report something sensible.
