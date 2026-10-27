# PAI (Personal Agent Inside Terminal)

An ultra-lightweight, module-decoupled, highly customizable CLI tool that leverages LLMs to assist with terminal tasks. PAI acts as your personal agent, helping you generate shell commands, answer questions, and perform multi-step DevOps workflows — all directly in the terminal.

## 📦 Installation

### Prerequisites
- Go 1.26 or later (or a prebuilt binary)
- API key for at least one supported LLM provider
- (Optional) Tavily API key for web search

### Install script
```bash
curl -fsSL https://raw.githubusercontent.com/Carudy/pai/main/install.sh | sh
```
Downloads the latest release, verifies it against `checksums.txt`, and installs
`pai` to `~/.local/bin` (override with `PAI_INSTALL_DIR`, pin with
`PAI_VERSION`). linux/amd64 and darwin/arm64 only.

### Prebuilt binaries
Each tagged release ships cgo-free binaries on the
[Releases](https://github.com/Carudy/pai/releases) page — Linux (amd64), macOS
(arm64), and Windows (amd64) — alongside a `checksums.txt`. Download one, make
it executable, and put it on your `PATH`; there are no runtime dependencies. Each
release also carries a version-independent name, so the newest binary can be
fetched directly without looking up the tag:

```bash
curl -fsSL -o pai https://github.com/Carudy/pai/releases/latest/download/pai_linux_amd64
# macOS: .../download/pai_darwin_arm64
chmod +x pai && mv pai ~/.local/bin/
```

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
| `pai tool` | `tools` | List, inspect, and dry-run custom tools |
| `pai service` | `services`, `svc` | Create and inspect resident service sessions |
| `pai serve` | — | Serve the browser UI and HTTP API (see [Browser / server mode](#-browser--server-mode)) |
| `pai conn` | — | Attach a terminal to a running `pai serve` session |
| `pai help [cmd]` | — | Show help |

Subcommands and their flags accept short forms, e.g. `pai session ls`,
`pai config set default_role coder`, `pai roles ls`.

```bash
pai config list                 # effective settings + the keys you can set
pai config get default_model    # print one value
pai config set default_role coder
pai config set context.keep_turns 6
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
keys, the search key, the `pai conn` token, and the hand-edited trusted lists
(`trusted_cmds`, `trusted_paths`).

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
# Long conversations: replay the last keep_turns user turns in full, and elide
# older command output down to its head and tail once the history reaches
# elide_after_tokens tokens (unset = half of summarize_after_tokens, or 100000 when
# there is no budget). One turn is a
# user instruction plus every tool call the model makes to answer it, so a whole
# turn is always kept. Elision is gated on size, not age: a rewrite invalidates the
# provider's cached prompt prefix from there to the end of the request. See "What
# gets sent to the model".
keep_turns       = 4
elide_min_bytes  = 1000
elide_head_lines = 8
elide_tail_lines = 4
# elide_after_tokens = 0
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
# Custom tool names that run without confirmation. A tool is trusted by name,
# never by the executable or endpoint it wraps. See "Custom tools" below.
# trusted_tools = ["run_eval", "gh_search"]
# Workspace roots whose .pai/tools/*.toml definitions may be loaded. A cloned
# repository is untrusted by default. Project tools are appended to every role
# in that workspace; their grants are session-only and never persisted.
# trusted_projects = ["~/work/myproject"]
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

[conn]
# Defaults for "pai conn", the terminal client for a running "pai serve".
# Flags and PAI_SERVE_URL / PAI_SERVE_TOKEN / PAI_SERVE_ORIGIN win over these.
url = "http://127.0.0.1:9384"   # empty also means this
# Prefer the environment variable; a token stored here is masked by
# `pai config list` and kept by `pai config reset`.
token = ""
# origin = "https://pai.example.com"  # only for a server with --public-origin

[session]
persist     = false  # true = save every run to an auto-named session
max_turns   = 0      # cap on turns replayed when resuming (0 = all)
recap_turns = 3      # recent exchanges echoed when resuming (0 = no recap)
# Snapshot a session into <name>.rewind-backup before a rewind truncates it
# (web "from here"). Default true.
# rewind_backup = true
```

The older `[app] truncate_exec_limit` / `truncate_search_limit` keys are no
longer read; a leftover key is ignored. Move those values to
`[context] exec_limit` / `search_limit`.

### Environment Variables

```bash
export DEEPSEEK_API_KEY="your-key"
export TAVILY_API_KEY="your-key"    # for web search

# pai conn: the server it attaches to, and its token (these win over [conn]).
export PAI_SERVE_URL="http://127.0.0.1:9384"
export PAI_SERVE_TOKEN="your-serve-token"
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

Requests are bounded so a silent provider cannot hang the run: 2 minutes to the
first byte of a streamed call, 90 seconds of silence inside a stream, and 10
minutes for a non-streaming completion. Transient 429/5xx/connection failures are
retried (up to 3 attempts, honouring `Retry-After` up to 15 seconds), and a stream
that stalls before showing anything is retried once. A stall after output has
appeared is reported rather than replayed, so partial answers are never
duplicated. `Ctrl+C` cancels immediately.

### Extending a role

Append your own instructions to any role's intro with
`~/.config/pai/role-extra.toml` — the text is added under a `[User:]` marker and
never replaces the intro, changes the response format, or redefines the tools.
The old `prompts.toml` was renamed to `role-extra.toml` and is always appended.

See [`internal/prompts/README.md`](internal/prompts/README.md) for the full
walkthrough, custom roles, and on-demand guides.

### Custom tools

A custom tool is a TOML file in `~/.config/pai/tools/<name>.toml` that wraps a
repeatable CLI action or one HTTP call. It has typed parameters, a fixed argv or
request, and an approval card that shows exactly what will run.

```toml
name        = "run_eval"
brief       = "run one evaluation dataset"
kind        = "cli"
description = "Run scripts/run_eval.py in the eval checkout."

[params]
dataset = { type = "string",  required = true }
folds   = { type = "integer", default = 5, min = 2, max = 10 }

[cli]
command = "uv"
args    = ["run", "scripts/run_eval.py", "--dataset", "{dataset}", "--folds", "{folds}"]
dir     = "."
timeout_seconds = 300
```

- The filename is the tool name. Built-in names are reserved; collisions are a
  load error.
- A role must list the tool in its `tools` array. To add it to a built-in role
  without copying the role, use `tools = ["run_eval"]` under that role's entry
  in `~/.config/pai/role-extra.toml`.
- Arguments are substituted verbatim; spaces, quotes, `;`, and `$(...)` stay
  inside one argument and are never interpreted by a shell.
- Trust is by tool name: add it to `[tool] trusted_tools`, or choose **trust
  this session/always** at the confirmation. The executable it wraps is not
  trusted.
- `pai tool ls` lists definitions, `pai tool show <name>` shows a definition and
  its source, and `pai tool check <name> --json '{"param":"value"}'` validates
  and prints the argv or HTTP request without executing it.

An HTTP tool pins a method and a literal host; parameters fill only the path,
query, or a JSON body. There is no retry and no redirect.

```toml
name  = "gh_search"
brief = "search GitHub repositories"
kind  = "http"

[params]
query = { type = "string",  required = true, max_length = 200 }
limit = { type = "integer", default = 5, min = 1, max = 20 }

[http]
method = "GET"
url    = "https://api.github.com/search/repositories?q={query}&per_page={limit}"
headers = { Accept = "application/vnd.github+json" }
auth    = "bearer GH_TOKEN"     # read from the environment at run time
max_bytes = 32768
# format = "json"               # pretty-printed JSON (default) or "text"
```

- The scheme and host are definition-owned; a placeholder there is a load error.
  Path values are path-escaped and query values query-escaped, so a parameter
  cannot add a path segment or another query pair.
- POST bodies come from `body_params = ["name", ...]` plus fixed `body_extra`
  values and are encoded as JSON, never assembled by string interpolation.
- Headers are fixed by the definition. `auth` names an environment variable
  (`bearer NAME` or `header NAME ENV`); values never appear in the approval card.
- Responses are capped at `max_bytes` (default 32 KiB). Non-2xx statuses,
  timeouts, and transport failures are observations with a status and a capped
  body; redirects are refused.

### Project tools

A trusted workspace contributes its own `<workspace>/.pai/tools/*.toml`
definitions. Add the workspace root (or a parent of it) to
`[tool] trusted_projects`:

```toml
[tool]
trusted_projects = ["~/work/myproject"]
```

- A cloned repository is untrusted by default, so its definitions cannot reach
  the model until you name its root. Both the workspace and the configured roots
  are symlink-resolved before the containment check, and neither the tools
  directory nor a definition file may be a symlink out of the workspace.
- Project tools are appended to every role used in that workspace; a role does
  not have to list them. A name that collides with a built-in or a user tool is
  a load error, not a silent override.
- Their grant is session-only: **always** is not offered, the session trust-all
  switch does not auto-approve them, and choosing trust never writes
  `trusted_tools`. The unit is still the tool name.

Copyable definitions for both layers live in
[`examples/tools/`](examples/tools): four project tools (local git/go and two
HTTP calls) and one user-layer tool, each with the payload to try.

## 📖 Roles

A role is data: an intro (its system prompt) plus the set of tools it may use.
There is a single agent loop — roles differ only by prompt and tool set. Three
roles ship built in, each a file under `internal/prompts/roles/`:

| Role | Purpose |
|---|---|
| `devops` | DevOps (default) — multi-step sysadmin tasks, local and remote shell |
| `coder` | Software engineering in the current repository |
| `latex` | Scientific writing and LaTeX, with keyless academic paper search |

A user role at `~/.config/pai/roles/<name>.toml` adds or overrides a role without a
rebuild. See [`internal/role/README.md`](internal/role/README.md) for the full role
catalogue (tool lists, remote-host management, paper search) and
[`internal/prompts/README.md`](internal/prompts/README.md) for how definitions are
resolved and parsed.

## 🔐 Trust and approvals

Tools that change things ask first. The terminal prompt and the browser approval
card offer the same choices; what is auto-approved is controlled by the lists below.

### Trusted commands

Commands whose first word matches the `trusted_cmds` list skip confirmation;
everything else prompts, and the confirmation offers run-once, skip, trust this
run, or trust from now on. Trust is by command *name*, so a construct, a file
write, a heredoc, or an embedded `$(…)` is never covered and always asks.

See [`internal/tool/README.md`](internal/tool/README.md) for the trust rules, the
confirmation keys, and the browser's per-session **Trust all** switch.

### Trusted paths (file tools)

By default every `edit` and `write` asks for approval; list directories in
`trusted_paths` to let changes under them apply straight away (the diff is still
shown). Paths are resolved — symlinks included — so a `..` or symlink cannot
escape a trusted directory, and an empty list confirms everything. Reads are not
confirmed unless `confirm_read = true`.

See [`internal/tool/README.md`](internal/tool/README.md) for path-trust resolution
and how it splits from command trust.

### Precise file operations

`edit` accepts a batch of disjoint changes to one file, each matched against the
original file and validated before anything is written; `read` and `write` stay
bounded and create-only, with guarded atomic publication.

See [`internal/role/README.md`](internal/role/README.md) for the batch shape and
[file-tool contracts](internal/role/filetools.md) for exact limits and safety.

## 🧠 What gets sent to the model

Each request is three parts:

```
[system: head]  +  conversation history  +  [system: per-turn reminder]
```

The **head** is composed once per session and then never changes, so provider
prefix caching stays effective:

1. who PAI is, plus your terminal info (OS, shell, user, when the session began,
   working directory)
2. the role's intro — extended by your `role-extra.toml` entry
3. the repository's instructions, when the role declares `context_files`
   (`AGENTS.md` / `CLAUDE.md`)
4. the role's tools, with descriptions and payload schemas
5. the response contract, with one example per action

The **per-turn reminder** is re-rendered on every request and deliberately comes
last, so the format is the final thing the model reads before it answers: a
one-line restatement of the contract plus the role's available tools. Its text is
byte-identical from one turn to the next — a clock there would break the cached
prefix on the Responses protocol, where the tail is folded into `instructions`
ahead of the history.

The response format is **system-owned**: neither a role's intro, your custom
prompt, nor a repository's `AGENTS.md` can change it — all three are marked
subordinate to it.

### Keeping the context bounded

Command output is capped by the `[context]` byte and line budgets, and long
conversations are compacted in two layers: deterministic, turn-windowed elision
of old command output, then optional summarization — never dropping user input,
replies, or the stored transcript.

See [`internal/chat/README.md`](internal/chat/README.md) for the compaction rules
and the session checkpoint.

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

Sessions live in the XDG data directory (`$XDG_DATA_HOME/pai/`, else
`~/.local/share/pai/`) in a pure-Go SQLite store, so no C toolchain is needed and
release binaries stay cross-compilable.

See [`internal/session/README.md`](internal/session/README.md) for the store,
locking, and backend details.

## 🔁 Resident services

A service is a session that wakes itself — on its own interval, or when a check
says the world changed — and then runs one bounded activation with the same loop
and role machinery as `pai chat`. Services run only while `pai serve` is running.

```bash
pai service create river-watch --file river.toml --yes
pai service show river-watch
pai serve
```

`pai service create` needs the session store, so stop `pai serve` first; the web
form (**Services** → **+ New session**) creates and schedules a service without a
restart.

### The manifest

```toml
[service]
goal             = "Watch the river gauge and report only when it matters."
role             = "devops"
workspace        = "~/work/river"
report_interval  = "15m"     # heartbeat; omit to wake only on events
report_on_change = true      # drop a heartbeat when no probe fired

[[watcher]]
name         = "river-level"
interval     = "60s"        # 10s .. 24h
timeout      = "10s"        # default min(10s, interval)
command      = "scripts/check_river"
prompt       = "If the level is high, say so and suggest checking the gate."
repeat_after = "10m"        # re-alert while true; omit = once per episode
severity     = "high"       # info|low|medium|high|critical

[[watcher]]
name     = "tide-sample"    # no command = a timer
interval = "13m"
prompt   = "Report the tide reading and whether it changed."
```

A watcher with a `command` is a predicate: the command prints `1`/`true` (plus
optional summary text) and the watcher decides. Without a `command` it is a
timer: it fires every interval and its `prompt` is the wake context. Both kinds
may carry a `prompt` — for a command probe it is extra guidance appended to the
output when the probe fires. The built-in `_report` heartbeat created by
`report_interval` carries one.

### Approving a command probe

A command watcher runs code on a schedule, so it needs a grant. The manifest is
stored either way; without `--yes` (or the form's checkbox) the probe is saved
unapproved and does not run:

- the web service panel shows the row as **awaiting approval · approve**; the
  button grants that one probe and it runs on the next tick;
- `pai service show` prints `awaiting approval`, and re-applying the manifest
  with `--yes` grants every command watcher it defines;
- a command name already trusted for the session — the trust-all switch, a
  **Trust session** answer, or `trusted_cmds` — counts as a grant and is recorded
  once. A probe built from a construct or a redirection has no command name, so
  it always needs the explicit grant.

A grant belongs to the probe's command: editing it clears the grant, so an
approval never silently covers code you have not seen. A timer needs none.

### Stop and start

```bash
pai service stop demo     # stop scheduling; the state survives a restart
pai service start demo    # resume and probe immediately
pai service list          # state column shows suspended / parked / stopped
```

While `pai serve` is running the store is locked, so use the **Stop** / **Start**
button in the service panel (or `POST /api/service/stop|start`). A stopped
service accepts no instructions until it is started.

### Changing settings

Model, role, and reasoning can be changed while a service is suspended (its
normal sleeping state), parked on a question, or stopped: the resident runtime is
retired and the next wake rebuilds with the new setting. A role change also
rewrites the manifest's `role`, so `pai service show` and a restart agree.
Workspace is the exception — probes and the model both run there, so it belongs to
the manifest; change it with a manifest edit while the service is stopped.

A **manifest edit** is a stopped-only replacement, not a live patch:

```bash
pai service stop demo
pai service edit demo --file river.toml --yes
pai service start demo
```

The web panel offers the same under **Details → Edit manifest** when the service
is stopped. The session's identity, history, queued events, and model are kept;
role, workspace, spec, and the watcher set are replaced. A probe whose spec did
not change keeps its latched state and its grant; a changed command starts clean
and needs a new approval (see above).

A user instruction is an immediate wake, not a wait for the next timer. The
head-of-activation probe pass is deferred for that wake, so the reply is not
queued behind commands; the deferred probes run on the following activation. The
instruction is recorded as a user turn, so it stays in the transcript and in the
history the model reads later.

### Probe contract

A `command` watcher is a predicate, not a parser: it runs in the workspace and
prints its decision on the first line.

| stdout | meaning |
|---|---|
| `1` or `true` | the condition holds |
| `0` or `false` | it does not |
| text after the token | the summary shown to the model |
| nonzero exit, empty output, unknown token | probe failure (reported once per distinct message) |

```sh
#!/bin/sh
set -eu
mode=$(python3 -c 'import json; print(json.load(open("state.json"))["mode"])')
[ "$mode" = "normal" ] && echo 0 || echo "1 mode=$mode"
```

`repeat_after` is the re-alert interval while the condition keeps holding; it is
measured from whichever came last, the fire or the model finishing with it, so a
long activation is not immediately re-alerted. Without it a watcher fires once
per continuous episode: the latch clears when the predicate goes false, so a
level condition cannot wake the model on every probe by accident. Debouncing a
flappy signal belongs in the probe script, where the condition's shape is known.
`report_on_change` makes `report_interval` mean "only when something changed".

While a fire is **outstanding** — it woke the model but that wake has not been
handled — the next situation lists it under `### Open items`. The watcher keeps
probing; with no `repeat_after` it stays quiet, and a due reminder refreshes the
still-pending event instead of queueing another. The list is derived from the
probe state, so there is no second queue to keep in sync.

The `goal` becomes a `## Service goal` block in the prompt head, after the role
and project guidance and before the tool reference, so every wake carries the
standing intent and compaction cannot drop it. `workspace` is the probe's and the
model's working directory, resolved once at creation.

See [`internal/watcher/README.md`](internal/watcher/README.md) for the probe and
state contract.

## 🌐 Browser / server mode

```bash
# Serve the current directory (localhost only by default).
pai serve --port 9384 --max-active-sessions 2
```

Open `http://127.0.0.1:9384`. The embedded frontend uses no CDN, JavaScript
framework, or Node build step. See `pai serve --help` for bind address, port,
authentication, and concurrency options.

### Using the UI

- The sidebar lists saved sessions; the `user@host` under the title names the
  machine serving the page, and the **Sessions** heading collapses the list (a
  per-browser preference, like the theme). Create a named session, select one,
  and use **Send**, **Steer**, or **Cancel**. Send queues a follow-up, Steer delivers at
  a safe step boundary, and Cancel interrupts current work without discarding
  queued tasks. Cancellation cannot undo completed side effects.
- A model question appears as an ordinary reply — answer it with your next
  instruction. Untrusted tools get explicit approval cards, including change
  previews and the same trust choices as the terminal (**Trust session** /
  **Always**).
- Multiple tabs may observe the same session, but there is only one runtime per
  session. Closing a tab does **not** cancel tasks; reconnect to recover history
  and pending approvals. A dropped event stream reconnects by itself (backing off
  to 30 s) and reloads the transcript; a control whose request fails re-reads the
  server's state rather than resending, so an instruction is never duplicated.
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
- The **Context** panel opens an inspector showing the exact message list the
  next send would use — the system head, one card per turn with what is actually
  sent (or why it is omitted), and the tail. See
  [`internal/web/README.md`](internal/web/README.md) for the projection rules and
  the few live-only exceptions (queued instructions, the steering marker).
- History loads lazily as you scroll up. Slash commands are not supported in the
  browser; use the sidebar and header controls instead.
- Any turn can be marked **☆ Essential** — a pill on a chat card, or the
  **Essential** control in the inspector. An essential turn is never elided and
  the summarizer is told to keep its content in full; the mark is revocable, and
  `★↑` / `★↓` in the inspector jump between marked cards.

### Attaching from the terminal

While `pai serve` is running, its session store is locked by the server, so a
second `pai chat` cannot open it. `pai conn` is the terminal counterpart of the
browser tab: it attaches over HTTP/SSE, renders the session's events in the same
terminal style as a local run, and accepts instructions, steering, and approvals.
It never opens the store, which is why it can run alongside the server.

```bash
pai conn demo                              # default http://127.0.0.1:9384
pai conn --url http://host:9384 -s demo    # or set PAI_SERVE_URL / PAI_SERVE_TOKEN
```

Defaults can live in `config.toml` instead, under `[conn]` (`url`, `token`,
`origin`); flags and the `PAI_SERVE_*` environment variables take precedence.

Mutations carry the URL's own origin, which is what the server's CSRF gate expects
from a same-origin browser; when the server requires a token, pass it with
`--token`/`PAI_SERVE_TOKEN`. If the server was started with a different
`--public-origin` (a reverse proxy), pass that with `--origin`.

Attaching to an idle session prints its stored transcript; attaching to a live
one streams events from that point, so an in-flight step is not printed twice.
A rejected token fails at once rather than retrying silently. Local commands:

| Command | Effect |
|---|---|
| `/sessions` | List the server's sessions, marking the attached one |
| `/inspect [N]` | Print the last N turns of the context that would be sent |
| `/trust [on\|off]` | Show or set auto-approval for this session |
| `/model [p:m]`, `/role [name]`, `/dir [path]` | Show options or switch, applied on the next send |
| `/steer <text>`, `/cancel` | Redirect the running task, or stop the current step |
| `/quit` | Detach (`Ctrl+C` cancels the remote step first) |

Any other `/command` is forwarded to the session, so in-session commands still
work; prefix `//` to send a literal instruction that starts with a slash.
Multiple terminals may attach to one session — both see the same events, and a
confirmation is answered by whichever client replies first.

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
cookies; forwarded headers are not trusted. To reach the same server directly as
well — by a LAN address, say — list that address with `--allow-origin
http://192.168.1.5:9384` (repeatable); it is accepted for mutations in addition
to the canonical origin, and you log in again there, because cookies are per
host. Do not expose plain HTTP carrying
a token to an untrusted network. The login authenticates access to tools that
can execute commands and modify files; this is a single-user service, not a
multi-tenant sandbox.

### Concurrency and workspaces

The concurrency limit bounds live runtimes; idle ones can be evicted to make
room, and full busy capacity rejects new work rather than queueing without bound.
Sessions run in their persisted working directory (not a sandbox), and while
serving PAI exclusively owns the session store.

See [`internal/runner/README.md`](internal/runner/README.md) for the residency and
workspace rules.

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

Scripts and other clients can drive the same sessions over HTTP — create, send,
steer, cancel, reply to approvals, switch role/model/workspace, rename, rewind,
and stream events over SSE. See [`internal/web/README.md`](internal/web/README.md)
for the full endpoint and security contract.

## 📚 Documentation map

- [`internal/role/README.md`](internal/role/README.md) — the agent loop, its
  runtime state, the built-in role catalogue, and precise file operations.
- [`internal/prompts/README.md`](internal/prompts/README.md) — role/tool TOML
  definitions, discovery, extending a role, and on-demand guides.
- [`internal/tool/README.md`](internal/tool/README.md) — execution primitives,
  command parsing, and the trusted-command / trusted-path rules.
- [`internal/chat/README.md`](internal/chat/README.md) — the LLM conversation
  protocol, prompt assembly, and context bounding.
- [`internal/session/README.md`](internal/session/README.md) — durable session
  storage, the SQLite backend, and store ownership.
- [`internal/runner/README.md`](internal/runner/README.md) — bounded concurrent
  runtimes, the integration contract, and workspace residency.
- [`internal/watcher/README.md`](internal/watcher/README.md) — the service probe
  contract, its debounce and latch state, and how a probe becomes a wake.
- [`internal/web/README.md`](internal/web/README.md) — the browser UI and the full
  HTTP/SSE API contract.

## 📄 License

MIT — see [LICENCE](LICENCE).

## 🙏 Acknowledgments

- [charmbracelet](https://github.com/charmbracelet) — Bubble Tea TUI, Lipgloss styling
- [Tavily](https://tavily.com) — Web search API
