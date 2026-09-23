# PAI (Personal Agent Inside Terminal)

An ultra-lightweight, module-decoupled, highly customizable CLI tool that leverages LLMs to assist with terminal tasks. PAI acts as your personal agent, helping you generate shell commands, answer questions, and perform multi-step DevOps workflows — all directly in the terminal.

## 📦 Installation

### Prerequisites
- Go 1.26 or later
- API key for at least one supported LLM provider
- (Optional) Tavily API key for web search

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
`trusted_cmds` must still be edited by hand. `list` masks secrets; `get` reveals
them.

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

- **Type while PAI works** — the text is queued and used as your next
  instruction, so you don't have to wait for a step to finish.
- **Ctrl+C** cancels the running step, or ends the session if nothing is running.
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
| `/role [name]` | Show or switch the active role |
| `/new [name]` | Start a fresh conversation, optionally named |
| `/rename <name>` | Name (and save) this conversation |
| `/compact` | Summarize older turns to shrink the context window |

Commands run locally and are not recorded as conversation turns. `/rename` on a
run started without `-s` saves the whole conversation so far under that name, so
a chat you decide to keep isn't lost. `/compact` is the exception that calls the
model — it summarizes older turns (see "Keeping the context bounded").

## ⚙️ Configuration

Create `~/.config/pai/config.toml` — or generate a documented starter file:

```bash
pai config init           # no config? writes one. Otherwise asks:
                          #   1) reset to defaults  2) add only what's missing  3) leave it
pai config init --merge   # add only settings you're missing (skip the prompt)
pai config init --reset   # back to defaults (skip the prompt)
pai config reset -y       # same as --reset, and keeps a .bak
```

`init` and `reset` never touch what you cannot easily recreate: provider API
keys, the search key, and `trusted_cmds` (an array the CLI can't set).

```toml
[providers]
deepseek = { api_key = "your-deepseek-key" }
# Unknown providers use OpenAI-compatible format.
# kimi   = { base_url = "https://api.moonshot.cn/v1/chat/completions", api_key = "your-kimi-key" }

[app]
default_model = "deepseek:deepseek-v4-flash"
default_role  = "devops"      # see `pai role ls` for the available roles
streaming     = true        # token-by-token output
reasoning     = "low"       # "low" | "medium" | "high" (omit for none)
interactive   = false       # if true, auto-enables -i mode

[context]
# Byte budget for one observation fed back to the model, and the lines kept
# from its head and tail (the tail holds errors and summaries).
exec_limit   = 8000
search_limit = 8000
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
trusted_cmds = [
    "ls", "cat", "grep", "pwd", "which",
]

[session]
persist     = false  # true = save every run to an auto-named session
max_turns   = 0      # cap on turns replayed when resuming (0 = all)
recap_turns = 3      # recent exchanges echoed when resuming (0 = no recap)
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

#### Trusted Commands
Commands matching the `trusted_cmds` list skip confirmation:
```toml
[tool]
trusted_cmds = ["ls", "cat", "grep", "pwd", "which", "df", "ps", "head", "tail"]
```

### `coder` — Software engineering
Helps read, write, refactor, and test code in the current repository. Tools:
- **execute** — Inspect and modify the repo, run builds and tests
- **websearch** — Look up libraries, APIs, and error messages

It deliberately has no **remote** tool: a role's tool list is its capability
boundary, not just a prompt hint.

It also folds the repository's own instructions into its system prompt: if
`AGENTS.md` (or `CLAUDE.md`) exists in the working directory or any parent, its
contents are appended as *project instructions* — subordinate to the role rules
and to the response format. See `context_files` under Custom roles.

```bash
pai -r coder "why does the build fail, and fix it"
```

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

`tools` may only reference built-in tools (`execute`, `remote`, `websearch`):
tool *implementations* live in Go, so new tools require code — new roles do not.

`context_files` folds project instruction files into the system prompt, searched
upwards from the working directory (nearest match wins) and capped at 8 KiB:

```toml
context_files = ["AGENTS.md", "CLAUDE.md"]
```

PAI prints a one-line notice whenever it uses one, so repository content
influencing the model is never silent.

See [examples/](examples/) for detailed walkthroughs.

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

## 📄 License

MIT — see [LICENSE](LICENSE).

## 🙏 Acknowledgments

- [charmbracelet](https://github.com/charmbracelet) — Bubble Tea TUI, Lipgloss styling
- [Tavily](https://tavily.com) — Web search API
