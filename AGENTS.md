# AGENTS.md — notes for coding agents

This file is for AI coding agents (and humans) working on **PAI** (*Personal
Agent Inside Terminal*): a lightweight Go CLI that runs an LLM agent loop
against a data-defined *role* to do terminal/DevOps and coding tasks.

Read [`internal/organization.md`](internal/organization.md) for the module map.
This file covers how to build, test, and where things belong.

## Mental model

- **One loop, data-defined roles.** `internal/role` is the only agent loop.
  A role is a TOML file (`internal/prompts/roles/<name>.toml`) = an intro + a
  tool list. Adding a role means adding a file, not Go code.
- **`cli` is the composition root.** Only `cli` imports `tui` and `session`.
  The loop talks to `core` ports; adapters implement them.
- **The model speaks JSON.** Every response is one JSON object with an
  `action` (`tool` | `ask` | `done` | `terminate`). `chat.OutputGuide()` is
  re-rendered after the history each turn so the format can't be forgotten.

## Commands

```bash
make build        # SQLite backend (default)
make build-file   # JSONL backend (-tags filestore), ~4 MB smaller
make test         # both tag sets
make vet          # both tag sets
make fmt          # gofmt
make deps         # pre-fill the module cache
make install      # go install ./cmd/pai
```

Always validate on **both** tag sets: `go build ./... && go build -tags filestore ./...`
(plus `vet`/`test`). A change that compiles under one backend may not under the other.

## How to test

Unit tests are thin — prefer a live smoke test, which is what actually catches
prompt/protocol bugs.

**Live smoke test** (never let it touch your real config):

```bash
mkdir -p /tmp/paihome/.config/pai
printf 'interactive = false\n' > /tmp/paihome/.config/pai/config.toml
go build -o /tmp/pai ./cmd/pai
HOME=/tmp/paihome /tmp/pai "list the current dir"
```

Gotchas that waste time if ignored:

- **Set `interactive = false`** in the temp config. If it's `true`, every run
  ends by *blocking at an input prompt* — which looks like a hang in automation
  but is correct behaviour.
- **Don't run `go` with a temp `HOME`** in the same command: Go will try to
  re-download the world. Build first, then run the binary with the temp `HOME`.
  If you must, pin `GOMODCACHE`/`GOPATH`/`GOCACHE` explicitly.
- **Never test against a real session store or API keys.** Use the temp `HOME`.
- Clean up `/tmp/paihome` and stray `pai` processes afterwards.

**If `go build`/`vet`/`test` seems to hang for ~30s:** it's the module proxy
being unreachable (blackholed), not compilation. Pre-fill the cache once:

```bash
HTTPS_PROXY=http://127.0.0.1:7890 HTTP_PROXY=http://127.0.0.1:7890 go mod download
```

Use plain `go mod download` (no `all`) — `all` widens `go.sum` beyond what
`go mod tidy` keeps, producing a spurious diff. `make deps` wraps this.

## Style to preserve

- **Comments explain *why*, not *what*.** The codebase documents intent,
  invariants, and tradeoffs (e.g. "rendered after the history so it's the last
  thing the model reads"). Match that; don't narrate obvious code.
- **Small, single-purpose packages.** Keep the layering intact — see the
  dependency rules in `internal/organization.md`. In particular, never import
  `tui` or `session` from `chat`/`role`/`tool`.
- **Ports carry meaning, not presentation.** Don't put ANSI/lipgloss concerns in
  `core`; the adapter decides rendering.
- **Validate at the boundary; feed failures back as data.** A tool handler does
  not error on bad model output — it returns a `[tool error]` observation so the
  model can self-correct.
- **Never hardcode role or tool lists.** Generate them from `prompts.RoleNames()`
  / `prompts.ToolNames()` (help text, validation, errors). A stale hardcoded list
  was a real past bug.
- **Wrap errors with context** (`fmt.Errorf("...: %w", err)`), and keep messages
  lowercase and specific.

## Where to change what

| Task | Touch |
|---|---|
| New **role** | add `internal/prompts/roles/<name>.toml` (no Go) |
| New **tool** | implementation in `internal/role/tools.go` **and** `internal/prompts/tools/<name>.toml`; `checkToolCoverage` enforces both exist. The `internal/tool` layer only *executes* — confirming with the user is the handler's job, via `rt.Prompter` |
| New **config key** | `internal/config/types.go` (struct + default) **and** the `configKeys` table in `internal/cli/config.go` (so `pai config set` knows it) |
| New **subcommand** | add to the table in `internal/cli/command.go`; put handlers in a sibling file (`config.go`, `role.go`, …) |
| New **in-session `/command`** | add to `commandSpecs` in `internal/role/command.go` — arity and an optional `check` hook are declared there, and `/help` is generated from it. Storage-touching commands use the `core.Sessions` port |
| New **session backend** | implement `session.Store` and select it by build tag in `session.Open` |

## Pitfalls

- **Tool payload shapes must match the handlers.** `execute` and `websearch`
  take a **string** payload; `remote` takes an **object**
  (`{"host":..., "cmd":...}`). Getting this wrong yields
  `json: cannot unmarshal object into Go value of type string` at runtime.
- **`role.Runtime` is per-run state**, not configuration — config lives in
  `config.UserConfig`. Don't mix them. The name `Session` is reserved for
  persistence (`session`); the loop's state is `Runtime`.
- **Two session backends use different on-disk formats**; a session written by
  one isn't visible to the other.

## Dependency & binary policy

- **Pure Go, cgo-free.** `go install github.com/Carudy/pai/cmd/pai@latest` must
  keep working, and the stripped binary stays small (~12 MB SQLite / ~8 MB JSONL).
  The SQLite backend uses `modernc.org/sqlite` precisely because it's pure Go.
- **Prefer existing dependencies.** Before adding one, prefer what's already
  used (`BurntSushi/toml`, the Charm stack) or the standard library. New
  dependencies need a clear justification against the size/complexity budget.
- **Subcommands over flag sprawl**, with short aliases, and generated help.
