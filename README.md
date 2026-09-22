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

## ⚙️ Configuration

Create `~/.config/pai/config.toml`:

```toml
[providers]
deepseek = { api_key = "your-deepseek-key" }
# Unknown providers use OpenAI-compatible format.
# kimi   = { base_url = "https://api.moonshot.cn/v1/chat/completions", api_key = "your-kimi-key" }

[app]
default_model = "deepseek:deepseek-chat"
default_role  = "devops"      # devops | coder
streaming     = true        # token-by-token output
reasoning     = "low"       # "low" | "medium" | "high" (omit for none)
interactive   = false       # if true, auto-enables -i mode
truncate_exec_limit   = 8000  # max command output chars fed back to the role
truncate_search_limit = 8000  # max web-search result chars fed back to the role

[tool]
tavily_api_key = "your-tavily-key"  # for web search (env TAVILY_API_KEY as fallback)
trusted_cmds = [
    "ls", "cat", "grep", "pwd", "which",
]

[session]
persist   = false   # true = save every run to an auto-named session
max_turns = 0       # cap on turns replayed when resuming (0 = all)
```

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

#### Web Search
The role automatically searches when it encounters unfamiliar terms or needs current info:
```bash
export TAVILY_API_KEY="your-key"
pai "what's the latest Kubernetes CVE and how do I patch it"
```

#### Trusted Commands
Commands matching the `trusted_cmds` list skip confirmation:
```yaml
trusted_cmds: ["ls", "cat", "grep", "pwd", "which", "df", "ps", "head", "tail"]
```

### `coder` — Software engineering
Helps read, write, refactor, and test code in the current repository. Tools:
- **execute** — Inspect and modify the repo, run builds and tests
- **websearch** — Look up libraries, APIs, and error messages

It deliberately has no **remote** tool: a role's tool list is its capability
boundary, not just a prompt hint.

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

See [examples/](examples/) for detailed walkthroughs.


## 💾 Sessions

By default PAI is stateless — nothing is written unless you ask for a session.

```bash
pai -s work "check nginx, then keep digging"   # create or continue "work"
pai --attach work "and now the disk usage"     # resume an existing session
pai -C "what did we find?"                     # resume the most recent session for this directory
```

A named session remembers the whole conversation, so a later run (a *different
process*) continues where you left off. Manage them with a subcommand:

```bash
pai session list                # list saved sessions
pai session show work           # details + recent turns
pai session rm work             # delete
pai session rename work ops     # rename
```

Set `[session] persist = true` to save *every* run to an auto-named session.

### Storage backend

Sessions live in the XDG data directory (`$XDG_DATA_HOME/pai/`, or
`~/.local/share/pai/`). Two backends implement the same interface:

| Backend | Build | Binary (stripped) |
|---|---|---|
| JSONL files (default) | `go build ./cmd/pai` | ~8.3 MB |
| SQLite (pure Go) | `go build -tags sqlite ./cmd/pai` | ~12.2 MB |

The default keeps `go install` dependency-free and light. `-tags sqlite` uses
pure-Go SQLite (`modernc.org/sqlite`) — still cgo-free, so `go install` keeps
working, at ~4 MB more. `make build` and `make build-sqlite` produce stripped
binaries. Note the two backends use different on-disk formats.

## 📄 License

MIT — see [LICENSE](LICENSE).

## 🙏 Acknowledgments

- [charmbracelet](https://github.com/charmbracelet) — Bubble Tea TUI, Lipgloss styling
- [Tavily](https://tavily.com) — Web search API
