# PAI (Personal Agent Inside Terminal)

An ultra-lightweight, module-decoupled, highly customizable CLI tool that leverages LLMs to assist with terminal tasks. PAI acts as your personal agent, helping you generate shell commands, answer questions, and perform multi-step DevOps workflows — all directly in the terminal.

## 📦 Installation

### Prerequisites
- Go 1.26 or later (or a prebuilt binary)
- API key for at least one supported LLM provider
- (Optional) Tavily API key for web search

### Prebuilt binaries
Each tagged release ships cgo-free binaries on the
[Releases](https://github.com/Carudy/pai/releases) page — Linux (amd64), macOS
(arm64), and Windows (amd64) — alongside a `checksums.txt`. Download one, make
it executable, and put it on your `PATH`; there are no runtime dependencies.

### Build from Source
```bash
git clone https://github.com/Carudy/pai.git
cd pai
go build -o pai ./cmd/pai
mv ./pai ~/.local/bin/

# Or by go install
go install github.com/Carudy/pai/cmd/pai@latest
```

## 🚀 Quick Start

```bash
# Show help
pai -h

# DevOps: multi-step task (the default role)
pai "check disk usage, find top 5 largest directories in /var/log"

# Coder: software-engineering tasks in the current repo
pai -r coder "add a --verbose flag to the CLI"

# Interactive session
pai -i

# DevOps with web search
pai "what's the latest Kubernetes LTS version and what CVEs affect it"

# Browser UI for named sessions
pai serve
```

## 🧭 Commands

With no command, `pai` chats, so `pai <input>` is shorthand for `pai chat <input>`.
Each command has a short alias, and every command has its own `--help`:

| Command | Alias | Purpose |
|---|---|---|
| `pai chat` | — | Talk to a role (the default action) |
| `pai session` | `sess` | List, show, rename, delete saved sessions |
| `pai config` | `cfg` | Inspect and edit `config.toml` |
| `pai role` | `roles` | List and inspect available roles |
| `pai serve` | — | Serve the browser UI and HTTP API (see [Browser / server mode](#-browser--server-mode)) |
| `pai help [cmd]` | — | Show help |

Subcommands and their flags accept short forms, e.g. `pai session ls`,
`pai config set default_role coder`, `pai roles ls`.

```bash
pai config list                 # effective settings + the keys you can set
pai config get default_model    # print one value
pai config set default_role coder
pai config set context.keep_turns 12
pai config unset default_role   # revert to the built-in default
pai config path                 # where config.toml lives
pai config init                 # write/merge a starter config.toml (asks first)
```

`pai config set` edits `config.toml` in place and preserves comments. Values are
validated (booleans, integers, `provider:model`, known roles), and arrays like
`trusted_cmds` and `trusted_paths` must still be edited by hand.
`preset_models` is set as a comma-separated list of `provider:model` entries.
`list` masks secrets; `get` reveals them.

Chat flags:

| Flag | Meaning |
|---|---|
| `-r, --role <name>` | Role to run (default from config) |
| `-m, --model <p:m>` | Override the model for this run |
| `-s, --session <name>` | Use or create a named session |
| `--attach <name>` | Resume an existing session |
| `-C, --continue` | Resume the most recent session (this directory first) |
| `--no-session` | Do not persist this run |
| `-i, --inter` | Multi-turn interactive chat |

Global `-d/--debug`, `-v/--version`, and `-h/--help` may appear before or after a
subcommand (`pai -d session ls`).

### Interactive mode

`pai -i` (or `interactive = true`) opens an input bar at the bottom of the
terminal while output scrolls above it. On a real terminal this is a full inline
UI; pipes and redirected output fall back to plain line prompts.

- **Type while PAI works**, then choose how it lands:
  - **Enter** *queues* it — delivered when PAI next stops for input;
  - **Ctrl+G** *steers* it — delivered at the next step boundary, so it can
    redirect the current job without waiting for it to finish;
  - **Ctrl+C** *interrupts* — it cancels the running step (killing the command
    and anything it spawned) and sends whatever you typed immediately.
- **Long commands show their elapsed time**: while a tool runs, the live line
  reads `⏳ running nix profile upgrade… 2m14s`, so a slow build is
  distinguishable from a hang.
- **Tool confirmations are modal**: `y`/`Enter` runs, `n`/`Esc` skips, `Ctrl+C`
  aborts, and other keys are ignored.
- The current session rides along in the live region: `[work]`, or
  `[<temp session>]` for a run that isn't saved.

### In-session commands

A line starting with `/` is a command, not a message to the model; a line
starting with `//` is sent literally, with one slash removed.

| Command | Does |
|---|---|
| `/help` (`/h`, `/?`) | List the available commands |
| `/exit` (`/quit`, `/q`) | End this session |
| `/info` (`/status`) | Session, role, model, and turn count |
| `/tools` | The active role's tools |
| `/trusted` | Show commands and paths trusted this run or from config |
| `/role [name]` | Show or switch the active role (saved to the session) |
| `/new [name]` | Start a fresh conversation, optionally named |
| `/rename <name>` | Name (and save) this conversation |
| `/compact` | Summarize older turns to shrink the context window |

Commands run locally and are not recorded as conversation turns. `/rename` on a
run started without `-s` saves the whole conversation so far under that name, so
a chat you decide to keep isn't lost. `/compact` is the exception that calls the
model — it summarizes older turns (see "Keeping the context bounded").

A session carries its role: switching with `/role <name>` saves the new role to
the session (when it is persisted), so reattaching later resumes the same role.
The switch changes the system prompt, so that turn is not prefix-cached.

## ⚙️ Configuration

Create `~/.config/pai/config.toml` — or generate a documented starter file:

```bash
pai config init           # no config? writes one. Otherwise asks:
                          #   1) reset to defaults  2) add only what's missing  3) leave it
pai config init --merge   # add only settings you're missing (skip the prompt)
pai config init --reset   # back to defaults (skip the prompt)
pai config reset -y       # same as --reset, and keeps a .bak
```

`pai config init --merge` is the upgrade path: it adds every setting your file
lacks — including optional ones (shown commented, with their explanation) that a
newer pai introduced, so they appear without changing any behaviour. It is
idempotent, so running it after an upgrade is safe.

`init` and `reset` never touch what you cannot easily recreate: provider API
keys, the search key, and the hand-edited trusted lists (`trusted_cmds`,
`trusted_paths`).

```toml
[providers]
deepseek = { api_key = "your-deepseek-key" }
# Unknown providers use OpenAI-compatible format.
# kimi   = { base_url = "https://api.moonshot.cn/v1/chat/completions", api_key = "your-kimi-key" }
# Use OpenAI's Responses API (the Codex-style /responses protocol) instead of
# chat-completions:
# openai = { api_key = "sk-...", protocol = "responses" }

[app]
default_model = "deepseek:deepseek-v4-flash"
default_role  = "devops"      # see `pai role ls` for the available roles
streaming     = true        # token-by-token output
reasoning     = "low"       # "low" | "medium" | "high" (omit for none)
interactive   = false       # if true, auto-enables -i mode
# Extra provider:model suggestions offered by frontends (the web model dropdown).
# A suggestion list, never an allowlist; when default_model is unset, the first
# entry becomes the model. Set it with
# `pai config set preset_models deepseek:deepseek-v4-flash,openai:gpt-4o`.
# preset_models = ["deepseek:deepseek-v4-flash", "openai:gpt-4o"]

[context]
# Byte budget for one observation fed back to the model, and the lines kept
# from its head and tail (the tail holds errors and summaries).
exec_limit   = 8000
search_limit = 8000
# Academic metadata/abstracts: separate complete-record budget (bytes).
# paper_limit = 16384
head_lines   = 80
tail_lines   = 40
# Long conversations: replay the last keep_turns messages in full, and elide
# older command output down to its header once the conversation exceeds
# elide_after_turns messages. See "What gets sent to the model".
keep_turns        = 8
elide_after_turns = 16
elide_min_bytes   = 1000
elide_head_lines  = 8
# Opt-in second layer: summarize the oldest turns once the prompt exceeds this
# many tokens (0 = off). Also on demand in-session with /compact.
summarize_after_tokens = 0

[tool]
tavily_api_key = "your-tavily-key"  # for web search (env TAVILY_API_KEY as fallback)
# Paper search needs no API key. Optional default source and Crossref contact:
# papersearch_source = "arxiv"      # arxiv | crossref
# papersearch_email = "you@example.org"
trusted_cmds = [
    "ls", "cat", "grep", "pwd", "which",
]
# Directories where the file tools (edit, write) apply without confirmation —
# your "trusted working place". A leading ~ is expanded; empty = confirm every
# change. Prefer an absolute path over "." so a run from an unexpected
# directory is not silently trusted.
trusted_paths = ["~/work/myproject"]
# Also confirm reads of files outside trusted_paths (off by default: reads are
# non-destructive, but this stops a file outside the workspace reaching the model
# unprompted).
confirm_read = false
# Kill a single command (local or remote) after this many seconds. 0 (default)
# = no timeout: Ctrl+C stops a running command, so long builds are not cut off.
cmd_timeout_seconds = 0
# Run remote commands through a login shell so the remote PATH/env loads, e.g.
# "bash" -> bash -lc '<cmd>'. Empty (default) = plain ssh. See Remote Host Management.
# remote_shell = "bash"

[session]
persist     = false  # true = save every run to an auto-named session
max_turns   = 0      # cap on turns replayed when resuming (0 = all)
recap_turns = 3      # recent exchanges echoed when resuming (0 = no recap)
# Snapshot a session into <name>.rewind-backup before a rewind truncates it
# (web "from here"). Default true.
# rewind_backup = true
```

The older `[app] truncate_exec_limit` / `truncate_search_limit` keys still work
as aliases, but `[context]` is canonical.

### Environment Variables

```bash
export DEEPSEEK_API_KEY="your-key"
export TAVILY_API_KEY="your-key"    # for web search
```

### Model Format

`provider:model_name` — e.g. `deepseek:deepseek-chat`, `openai:gpt-4o`, `doubao:doubao-1-5-pro-32k`.

### Protocols

Every provider speaks the OpenAI-compatible **chat completions** API by default.
Set `protocol = "responses"` in a provider's section to use OpenAI's
**Responses API** (`POST /responses`) instead — the protocol Codex-style clients
use. `base_url` is honored under either protocol and must be the complete
endpoint URL. The built-in names (`deepseek`, `mistral`, `openai`) each carry a
default endpoint; only `openai` has a built-in `/responses` endpoint, so any other
provider needs `base_url` set for `responses`. The Responses transport is
stateless (`store = false`): the whole conversation is replayed each turn.
Reasoning still works, but the API returns a model-written *summary* rather than
raw chain of thought, and only when `reasoning` is enabled.

A provider may also carry a `[providers.<name>.headers]` sub-table of extra HTTP
headers (for example a custom `User-Agent`). PAI always sends a `User-Agent` and,
for the OpenCode Go gateway (`opencode.ai`), generates the `x-opencode-session`
routing id that gateway requires; a header you set with the same name wins.

### Custom Prompts

Create `~/.config/pai/prompts.toml` to customize a role's behavior. The custom
text is applied to the role's *intro* only — the response format is fixed by the
app so a custom prompt can never break parsing.

```toml
[devops]
additional = false       # false = replace the role intro, true = append to it
prompt = """
You are a senior SRE. Always explain why before running commands.
"""
```

## 📖 Roles

A role is data: an intro (its system prompt) plus the set of tools it may use.
There is a single agent loop — roles differ only by prompt and tool set.

### `devops` — DevOps (default)
Autonomous reason→act→observe loop for multi-step sysadmin tasks. Tools:
- **execute** — Run local shell commands
- **remote** — Run commands on remote servers via SSH
- **read** — Read a text file (numbered, bounded) instead of `cat`
- **edit** — Replace exact text in a file, with a diff to approve
- **write** — Create a new file (never overwrites) without a shell here-doc
- **websearch** — Search the web for current information (config `tavily_api_key` or `TAVILY_API_KEY` env)


```bash
pai "deploy my app to staging"
pai -i                        # Interactive: keep loop alive for follow-ups
```

#### Remote Host Management
Configure hosts in `~/.ssh/config` as usual:
```bash
pai "check nginx status on myserver"
# Connections are cached via SSH ControlMaster — no re-auth between commands.
```

ssh runs the command through the remote login shell non-interactively, so
profile files aren't sourced and the remote `PATH`/env may be missing. This is
common with **nix** and asdf, which set `PATH` from `/etc/profile` (a file fish,
for example, never reads — hence `fish: Unknown command: netbird`). Point pai at
a *login* POSIX shell and it wraps each command accordingly:
```bash
pai config set remote_shell bash   # → bash -lc '<cmd>'
```
A bare name gets `-lc` appended; set a value containing a space (e.g.
`"bash -lc"`) to use it verbatim as the prefix.

Leave `remote_shell` empty (the default) to keep plain ssh behaviour. Only set a
shell that exists on the remote host.

#### Web Search
The role automatically searches when it encounters unfamiliar terms or needs current info:
```bash
export TAVILY_API_KEY="your-key"
pai "what's the latest Kubernetes CVE and how do I patch it"
```

### `coder` — Software engineering
Helps read, write, refactor, and test code in the current repository. Tools:
- **execute** — Inspect and modify the repo, run builds and tests
- **read** — Read a file region with line numbers (preferred over `cat`)
- **edit** — Replace exact text in a file, reviewed as a diff before it is written
- **write** — Create a new file from data, not a shell here-doc (never overwrites)
- **websearch** — Look up libraries, APIs, and error messages
- **papersearch** — Find research papers, original abstracts, and identifiers

It deliberately has no **remote** tool: a role's tool list is its capability
boundary, not just a prompt hint.

It also folds the repository's own instructions into its system prompt: if
`AGENTS.md` (or `CLAUDE.md`) exists in the working directory or any parent, its
contents are appended as *project instructions* — subordinate to the role rules
and to the response format. See `context_files` under Custom roles.

```bash
pai -r coder "why does the build fail, and fix it"
```

### `latex` — Scientific writing and LaTeX

Drafts, polishes, restructures, and debugs manuscripts while preserving scientific
meaning, equations, labels, citation keys, and existing project conventions.
Tools: **read**, **edit**, **write**, **execute**, **websearch**, **papersearch**;
no remote execution. It reads project instructions from `AGENTS.md`.

```bash
pai -r latex "polish the introduction without changing the scientific claims"
pai -r latex "fix undefined citations using the project's existing build"
```

The role distinguishes verified bibliography metadata from missing information,
never invents evidence, and does not equate abstracts with full-text reading.
Builds follow the project's engine/tooling and run non-interactively. Missing TeX
tools and pre-existing warnings are reported, not hidden; successful compilation
is not a visual PDF check. Shell escape requires explicit user authorization.

A local `~/.config/pai/roles/latex.toml` **overrides** this built-in role, including
its tool list. Keep it for customization; to use the built-in role instead, move
it out of the roles directory or rename it without the `.toml` extension.

### Paper Search

The `coder` and `latex` roles can discover academic papers without a search API
key; `devops` deliberately does not include this tool:

```bash
pai -r latex "find recent arXiv papers about retrieval augmented generation evaluation"
pai -r coder "find Crossref papers on distributed consensus and cite their DOIs"
pai config set papersearch_source crossref  # default when the model omits source
pai config set papersearch_email you@example.org  # optional Crossref polite pool
pai config set context.paper_limit 16384   # bytes of formatted paper results
```

`papersearch` takes an object, unlike `websearch`'s string query:

```json
{"source":"arxiv","query":"retrieval augmented generation evaluation","limit":5,"year_from":2023,"sort":"relevance"}
```

- **Sources:** arXiv is strongest for CS/math/physics preprints; Crossref searches
  DOI/bibliographic metadata across disciplines, but abstracts are often absent.
  One provider is queried per call; neither is a comprehensive scholarly index.
- **Options:** `source` is optional (default `arxiv`, configurable); `query` is
  required plain-text keywords, not provider syntax. `limit` defaults to 5
  (maximum 20), `year_from`/`year_to` are inclusive, and `sort` is `relevance`
  or `newest`. arXiv dates refer to first submission; Crossref dates refer to
  publication. arXiv combines keywords with AND, so shorter queries may help.
- **Output:** original titles, authors, dates, identifiers, URLs, and available
  abstracts—not an extra AI summary. Abstracts are capped at 2,000 characters
  with a truncation marker. The separate 16,384-byte default budget omits whole
  records with a notice rather than severing their identifiers. No pagination,
  full-text/PDF reading, file downloads, or citation-graph traversal yet.
- **Network/privacy:** the explicit query goes to the selected public API;
  an optional contact email goes only to Crossref. No local files are included.
  Tool observations may be retained in persisted sessions. Keyless does not
  mean unlimited: requests share process-wide throttles (arXiv: one connection,
  starts at least three seconds apart; Crossref: one start per second), a
  45-second deadline, and one bounded retry for HTTP 429/503. Separate PAI
  processes do not share these throttles; coordinate them when using arXiv.
- **Evidence:** metadata/abstracts are untrusted source material, not instructions;
  missing abstracts are explicit. Preprints, DOIs, and publication types do not
  establish peer review or paper quality. The tool does not claim full-text access.

User role overrides must add `"papersearch"` to their `tools` list to enable it.
The existing Tavily-backed `websearch` remains unchanged.


### Custom roles

Roles are data, so you can add your own without rebuilding. Drop a file at
`~/.config/pai/roles/<name>.toml`:

```toml
name        = "writer"
description = "Technical writing assistant"

tools = ["execute", "websearch"]

intro = '''
You are a technical writer. Help draft, edit, and tighten prose.
Read whichever files you need with the execute tool before rewriting anything.
'''
```

Then run it with `pai -r writer`. A user role with the same name as a built-in
one **overrides** it — handy for retuning `devops` without editing the source.

`tools` may only reference built-in tools (`execute`, `remote`, `websearch`,
`papersearch`, `read`, `edit`, `write`): tool *implementations* live in Go, so new tools require
code — new roles do not.

`context_files` folds project instruction files into the system prompt, searched
upwards from the working directory (nearest match wins) and capped at 8 KiB:

```toml
context_files = ["AGENTS.md", "CLAUDE.md"]
```

PAI prints a one-line notice whenever it uses one, so repository content
influencing the model is never silent.

See [examples/](examples/) for detailed walkthroughs, and
[Trust and approvals](#-trust-and-approvals) for how tool calls are confirmed.

## 🔐 Trust and approvals

Tools that change things ask first. The terminal prompt and the browser approval
card offer the same choices; what is auto-approved is controlled by the lists below.

### Trusted commands
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

### Trusted paths (file tools)
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

### Precise file operations

`edit` supports several disjoint changes to **one file** in one reviewed call:

```json
{"path":"main.go","edits":[{"old_string":"first original block","new_string":"first replacement"},{"old_string":"second original block","new_string":"second replacement"}]}
```

- Each entry matches the **original file**, not earlier replacements. All entries
  must validate before anything is written; overlapping ranges are rejected.
  The original top-level `old_string`/`new_string`/`replace_all` shape still works,
  but cannot be mixed with `edits`. Batches allow 1–50 edits and 10000 replacement
  sites at most. Missing replacement fields are errors, not accidental deletions.
- Matching is exact except for CRLF/LF representation. Untouched bytes and UTF-8
  BOM are preserved; indentation and Unicode punctuation are not fuzzy-matched.
  Previews show full affected lines with context, mark truncation, and are bounded.
- After approval, edits recheck file/parent identity, contents, and permissions.
  If changed, the model must read again and request a fresh edit. `write` remains
  create-only, with atomic no-clobber publication even if a path appears during
  approval. Failed operations do not grant directory trust.
- `read` returns complete numbered lines up to 2000 lines or `context.exec_limit`
  bytes (default 8000). Its continuation offset follows what was actually shown;
  oversized first lines receive an actionable notice instead of partial text.
  Total line counts appear only when known. Strip only the line number and tab
  when copying text for an edit.
- File tools handle regular UTF-8 text, not devices/directories or binary data.
  Mutations are limited to 16 MiB and refuse final-path symlinks; explicitly edit
  the real target instead. Parent directories must exist. Create-only publication
  requires filesystem hard-link support and fails safely if unavailable.

These checks protect against stale approvals and serialize PAI's own commits;
they are not a filesystem sandbox or a portable lock against external writers.
Atomic edits preserve permission bits, not inode identity, ownership, or extended
attributes. See [file-tool design](internal/role/filetools.md) for the upstream
comparison, exact contracts, and remaining limitations.

## 🧠 What gets sent to the model

Each request is three parts:

```
[system: head]  +  conversation history  +  [system: per-turn reminder]
```

The **head** is composed once per session and then never changes, so provider
prefix caching stays effective:

1. who PAI is, plus your terminal info (OS, shell, user, time, working directory)
2. the role's intro — replaced or extended by your `prompts.toml` entry
3. the repository's instructions, when the role declares `context_files`
   (`AGENTS.md` / `CLAUDE.md`)
4. the role's tools, with descriptions and payload schemas
5. the response contract, with one example per action

The **per-turn reminder** is re-rendered on every request and deliberately comes
last, so the format is the final thing the model reads before it answers: a
one-line restatement of the contract plus the role's available tools.

The response format is **system-owned**: neither a role's intro, your custom
prompt, nor a repository's `AGENTS.md` can change it — all three are marked
subordinate to it.

### Keeping the context bounded

Command output is shortened before it goes back to the model: the `[context]`
section's `exec_limit` / `search_limit` cap the bytes, while `head_lines` /
`tail_lines` decide how much of the head and tail survives. The tail is kept on
purpose — errors, exit codes and summaries land at the end of command output.

Long conversations are compacted in two layers. The first is deterministic:
the last `keep_turns` messages are replayed verbatim; once the conversation
exceeds `elide_after_turns` messages, older command output larger than
`elide_min_bytes` is reduced to its header (label, command, exit status) with a
visible marker, so the model knows it was omitted and can re-run the command if
it needs the result.

The second layer is optional and costs one extra model call: with
`summarize_after_tokens` set, once the prompt exceeds that token budget the
oldest turns are summarized into a single message and only the last `keep_turns`
messages are kept verbatim. Summarization uses a plain call — no streaming, no
reasoning, no JSON mode. Trigger it on demand in-session with `/compact`.

In both layers user input, assistant replies and answers are never dropped, and
the full transcript stays in the session store — compression only changes what
is *replayed* to the model.

A summary is also written to the session as a **checkpoint** (a `system` turn of
kind `summary`). Attaching later replays that summary and only the turns after
it, so a long session resumes without re-summarizing — while every original turn
is still on disk (visible in `pai session show`).

## 💾 Sessions

By default PAI is stateless — nothing is written unless you ask for a session.

```bash
pai -s work "check nginx, then keep digging"   # create or continue "work"
pai --attach work "and now the disk usage"     # resume an existing session
pai -C "what did we find?"                     # most recent session (this directory first)
```

A named session remembers the whole conversation, so a later run (a *different
process*) continues where you left off. Pass `--no-session` to force a one-off run
even when a session would otherwise apply. Manage them with a subcommand:

```bash
pai session ls                  # list saved sessions (list also works)
pai session show work           # details + recent turns
pai session rm work             # delete
pai session rename work ops     # rename
```

Set `[session] persist = true` to save *every* run to an auto-named session.

Resuming prints a short recap of the last few exchanges so you have context
(`[session] recap_turns`, default 3; `0` turns it off).

### Storage backend

Sessions live in the XDG data directory (`$XDG_DATA_HOME/pai/`, or
`~/.local/share/pai/`). Two backends implement the same interface:

| Backend | Build | Binary (stripped) |
|---|---|---|
| SQLite (default) | `go build ./cmd/pai` | ~12 MB |
| JSONL files | `go build -tags filestore ./cmd/pai` | ~8 MB |

The default uses pure-Go SQLite (`modernc.org/sqlite`) — cgo-free, so `go
install` keeps working. Pass `-tags filestore` for a ~4 MB smaller binary with no
SQLite dependency. `make build` / `make build-file` produce stripped binaries.
Note the two backends use different on-disk formats.

## 🌐 Browser / server mode

```bash
# Serve the current directory (localhost only by default).
pai serve --port 9384 --max-active-sessions 2
```

Open `http://127.0.0.1:9384`. The embedded frontend uses no CDN, JavaScript
framework, or Node build step. See `pai serve --help` for bind address, port,
authentication, and concurrency options.

### Using the UI

- The sidebar lists saved sessions. Create a named session, select one, and use
  **Send**, **Steer**, or **Cancel**. Send queues a follow-up, Steer delivers at
  a safe step boundary, and Cancel interrupts current work without discarding
  queued tasks. Cancellation cannot undo completed side effects.
- A model question appears as an ordinary reply — answer it with your next
  instruction. Untrusted tools get explicit approval cards, including change
  previews and the same trust choices as the terminal (**Trust session** /
  **Always**).
- Multiple tabs may observe the same session, but there is only one runtime per
  session. Closing a tab does **not** cancel tasks; reconnect to recover history
  and pending approvals.
- The header has **Role**, **Model**, and **Working dir** controls (each with
  **Apply**). Settings changes require an *idle* session (no running work,
  queued instructions, or pending approvals), are saved to the session, and take
  effect on the next send. Changing the directory preserves conversation history
  but rebuilds tools and project instructions against the new workspace; it does
  not move files. The directory must already exist on the server. Model suggestions
  include the configured default,
  `preset_models`, and models saved in sessions; you can also type
  `provider:model` for a configured provider. Switching preserves history and
  does not change the global default. **Rename** is also idle-only; the light/dark
  theme switch is remembered locally and works at any time.
- Every user instruction carries an **↩ from here** button: it rewinds the session
  to just before that instruction, drops the turns after it, and returns the
  instruction to the composer. The confirmation offers **Rewind + backup** (copies
  the dropped turns into a single rolling `<name>.rewind-backup` session first, so
  nothing is lost) or **Rewind** (truncate directly); `[session] rewind_backup`
  only sets the default when an API client omits the choice.
- History loads lazily as you scroll up. Slash commands are not supported in the
  browser; use the sidebar and header controls instead.

### Remote access

For a VPS, prefer an SSH tunnel or VPN. Non-loopback binds require a token of
at least 32 characters, supplied through `--token-file` or `PAI_SERVE_TOKEN`.
Use a private token file rather than putting credentials in URLs or arguments.
For an HTTPS reverse proxy:

```bash
pai serve --token-file /path/to/private-token \
  --public-origin https://pai.example.com
```

The proxy should preserve `Host` and support SSE without response buffering.
`--public-origin` explicitly configures browser origin validation and Secure
cookies; forwarded headers are not trusted. Do not expose plain HTTP carrying
a token to an untrusted network. The login authenticates access to tools that
can execute commands and modify files; this is a single-user service, not a
multi-tenant sandbox.

### Concurrency and workspaces

The concurrency limit bounds live runtimes; idle runtimes can be evicted to
make room. Full busy capacity rejects new work rather than growing an unbounded
queue. Sessions execute in their persisted working directory, defaulting to the
server's launch directory for new sessions. Directories must already exist and
are normalized to absolute paths; they are not security sandboxes, and no request
changes the process cwd. Use separate worktrees when concurrent tasks could
modify the same files.

While serving, PAI exclusively owns the session store across processes, for
both SQLite and JSONL. Other PAI processes cannot open that store until the
server stops. Do not delete `sessions.lock` while PAI is running. Tasks survive
browser disconnections, **not server restarts**; use a service manager such as
systemd for hosting. Shutdown cancels active work and releases store ownership.

### Activity log

`pai serve --activity-log` prints concise stdout activity, for example
`[my-session]: tool_exec execute /bin/sh ls -la`, plus lifecycle and prompt labels.
It is off by default and works without any browser connected. `tool_exec` announces
a tool call, which may still require approval; it does not guarantee execution.
Logs omit reasoning, answers, configuration, diffs, and raw tool output. **Commands
and targets may contain sensitive data**, including credentials: enable this only
when appropriate for your stdout destination. Events use a bounded 128-event queue;
slow output drops new events rather than blocking agents. Shutdown drains queued
logs for up to 250 ms; a blocked writer may lose the remaining logs.

### HTTP API

Scripts and other clients can drive the same sessions over HTTP — create a saved
session, send, steer, cancel, reply to approvals, switch role/model/workspace, rename,
rewind, and stream events over SSE. Mutating requests require JSON, a same-origin
`Origin` header, and the login cookie when authentication is enabled. The full
endpoint and security contract is in [`internal/web/README.md`](internal/web/README.md).

## 📄 License

MIT — see [LICENSE](LICENSE).

## 🙏 Acknowledgments

- [charmbracelet](https://github.com/charmbracelet) — Bubble Tea TUI, Lipgloss styling
- [Tavily](https://tavily.com) — Web search API
