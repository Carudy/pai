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

The older `[app] truncate_exec_limit` / `truncate_search_limit` keys still work
as aliases, but `[context]` is canonical.

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
- [`internal/web/README.md`](internal/web/README.md) — the browser UI and the full
  HTTP/SSE API contract.

## 📄 License

MIT — see [LICENCE](LICENCE).

## 🙏 Acknowledgments

- [charmbracelet](https://github.com/charmbracelet) — Bubble Tea TUI, Lipgloss styling
- [Tavily](https://tavily.com) — Web search API
