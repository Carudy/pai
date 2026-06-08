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

# Ask a question
pai -a qa "what is a Kubernetes pod"

# Generate a shell command
pai -a cmd "find all files larger than 100MB"

# Interactive multi-turn Q&A
pai -a qa -i

# DevOps: multi-step task
pai "check disk usage, find top 5 largest directories in /var/log"

# Private: masked math computation
pai -a private "what is <mask:abc> to the power of <mask:xyz>"

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
default_agent = "devops"    # cmd | qa | devops | private
streaming     = true        # token-by-token output
reasoning     = "low"       # "low" | "medium" | "high" (omit for none)
interactive   = false       # if true, auto-enables -i mode

[tool]
tavily_api_key = "your-tavily-key"  # for web search (env TAVILY_API_KEY as fallback)
trusted_cmds = [
    "ls", "cat", "grep", "pwd", "which",
]
```

### Environment Variables

```bash
export DEEPSEEK_API_KEY="your-key"
export TAVILY_API_KEY="your-key"    # for web search
```

### Model Format

`provider:model_name` — e.g. `deepseek:deepseek-chat`, `openai:gpt-4o`, `doubao:doubao-1-5-pro-32k`.

### Custom Prompts

Create `~/.config/pai/prompts.toml` to customize agent behavior:
```toml
[devops]
additional = false       # false = replace, true = append
prompt = """
You are a senior SRE. Always explain why before running commands.
"""

[qa]
additional = true
prompt = "Always answer in Chinese."
```

### Mask Database (for `private` agent)

Create `~/.config/pai/mask.toml` to map masked tokens to real values:
```toml
[mask]
abc = 42
xyz = 3.14
```

## 📖 Agents

### `cmd` — Command Generator
One-shot shell command generation with user-confirmed execution.
```bash
pai -a cmd "sum the second column of data.csv"
```

### `qa` — Question Answering
Single-turn or interactive multi-turn chat with a full Bubble Tea TUI.
```bash
pai -a qa "explain Docker layers"
pai -a qa -i                 # Interactive session
```

### `devops` — DevOps Agent
Autonomous reason→act→observe loop. Tools available:
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
The agent automatically searches when it encounters unfamiliar terms or needs current info:
```bash
export TAVILY_API_KEY="your-key"
pai "what's the latest Kubernetes CVE and how do I patch it"
```

#### Trusted Commands
Commands matching the `trusted_cmds` list skip confirmation:
```yaml
trusted_cmds: ["ls", "cat", "grep", "pwd", "which", "df", "ps", "head", "tail"]
```

### `private` — Masked Math
Computes expressions with privacy-preserving placeholders. Numbers are masked as `<mask:TOKEN>` and resolved from `~/.config/pai/mask.yml`.
```bash
pai -a private "what is <mask:abc> ** <mask:xyz> plus 10"
```

See [examples/](examples/) for detailed walkthroughs.


## 📄 License

MIT — see [LICENSE](LICENSE).

## 🙏 Acknowledgments

- [charmbracelet](https://github.com/charmbracelet) — Bubble Tea TUI, Lipgloss styling
- [Tavily](https://tavily.com) — Web search API
