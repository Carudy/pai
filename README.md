# PAI (Personal Agent Inside Terminal)

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.21+-blue.svg)](https://golang.org/)

An ultra-lightweight, module-decoupled, highly customizable CLI tool that leverages LLMs to assist with terminal tasks. PAI acts as your personal agent, helping you generate shell commands, answer questions, and perform multi-step DevOps workflows — all directly in the terminal.

## ✨ Features

- **Multi-Provider LLM Support**: Compatible with any OpenAI-compatible API (OpenAI, DeepSeek, Mistral, Kimi, Doubao, etc.)
- **Three Agent Modes**:
  - **cmd** — One-shot shell command generation with optional execution
  - **qa** — Question answering with single-turn or interactive multi-turn chat (Bubble Tea TUI)
  - **devops** — Autonomous reason–act–observe loop for multi-step sysadmin & ops tasks
- **Remote Host Management** — `devops` agent can run commands on remote servers via SSH with automatic connection caching (ControlMaster)
- **Interactive Command Execution** — Safe command generation with user confirmation before execution
- **Context-Aware** — Automatically detects OS, shell, working directory, and timestamp for better LLM responses
- **Custom Prompts** — Override or extend agent prompts via `~/.config/pai/prompts.yml`
- **Flexible Configuration** — Environment variables or YAML config for easy setup

## 📦 Installation

### Prerequisites
- Go 1.26 or later
- API key for at least one supported LLM provider

### Build from Source
```bash
git clone https://github.com/Carudy/pai.git
cd pai
go build -o pai ./cmd/pai
# Optional: Add to PATH
mv ./pai ~/.local/bin/

# Or by go install
go install github.com/Carudy/pai/cmd/pai@latest
```

## 🚀 Quick Start

```bash
# Check version
pai -v

# Show help
pai -h

# Ask a question (single turn)
pai -a qa "what is a Kubernetes pod"

# Generate a shell command
pai -a cmd "find all files larger than 100MB"

# Interactive Q&A session
pai -a qa -i

# DevOps: multi-step task
pai "check disk usage, find top 5 largest directories in /var/log, and suggest cleanup"
```

## ⚙️ Configuration

PAI supports configuration via environment variables or a config file.

### Environment Variables
```bash
export OPENAI_API_KEY="your-openai-key"
export DEEPSEEK_API_KEY="your-deepseek-key"
export MISTRAL_API_KEY="your-mistral-key"
```

### Config File
Create `~/.config/pai/config.yml`:
```yaml
providers:
  deepseek:
    api_key: "your-deepseek-key"
  mistral:
    api_key: "your-mistral-key"
  # Unknown providers (e.g. kimi, doubao) use OpenAI-compatible format.
  # base_url must be the complete chat completions endpoint URL.
  # kimi:
  #   base_url: "https://api.moonshot.cn/v1/chat/completions"
  #   api_key: "your-kimi-key"
  # doubao:
  #   base_url: "https://ark.cn-beijing.volces.com/api/v3/chat/completions"
  #   api_key: "your-doubao-key"

default_model: "deepseek:deepseek-chat"
default_agent: "devops"   # can be "cmd", "qa", or "devops"
streaming: true           # token-by-token streaming (default: false)
reasoning: low            # thinking/reasoning effort: low, medium, high, or omit for none
```

> **Note**: Model format is `provider:model_name` (e.g. `deepseek:deepseek-chat`, `openai:gpt-4o`, `doubao:doubao-1-5-pro-32k`).

### Custom Prompts
Create `~/.config/pai/prompts.yml` to customize agent behavior:
```yaml
devops:
  additional: false       # false = replace, true = append
  prompt: |
    You are a senior SRE. Always explain why before running commands.
qa:
  additional: true
  prompt: |
    Always answer in Chinese.
```

## 📖 Agents

### `cmd` — Command Generator
One-shot shell command generation with user-confirmed execution.
```bash
pai -a cmd "sum the second column of data.csv"
```

### `qa` — Question Answering
Single-turn or interactive multi-turn chat with a full TUI.
```bash
pai -a qa "explain Docker layers"
pai -a qa -i                 # Interactive session (Bubble Tea chatbox)
```

### `devops` — DevOps Agent
Autonomous reason→act→observe loop for complex multi-step tasks.
```bash
pai "deploy my app to staging"
pai -i                        # Interactive mode: keep the loop alive for follow-ups
```

#### Remote Host Management
The devops agent can run commands on remote servers. Configure hosts in `~/.ssh/config` as usual, then:
```bash
pai "check nginx status on myserver"
# The AI will use the "remote" action — connections are cached via SSH ControlMaster
```

See [examples/](examples/) for detailed walkthroughs.

## 🛣️ Todo

- [ ] MCP / tools integration
- [ ] Local database for sessions, memory, etc.
- [ ] DevOps enhancements (RAG/fuzzy CLI tool discovery)
- [ ] Plugin system for custom agents

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- [charmbracelet](https://github.com/charmbracelet)
  - [Bubble Tea](https://github.com/charmbracelet/bubbletea): Terminal UI framework
  - [Lipgloss](https://github.com/charmbracelet/lipgloss): Style definitions for terminal output
