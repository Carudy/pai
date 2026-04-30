# PAI (Personal Agent Inside Terminal)

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.21+-blue.svg)](https://golang.org/)

An ultra-lightweight, module-decoupled, highly customizable CLI tool that leverages LLMs to assist with terminal tasks. PAI acts as your personal agent, helping you generate shell commands, answer questions, and perform multi-step DevOps workflows — all directly in the terminal.

## ✨ Current Features

- **Multi-Provider LLM Support**: Compatible with OpenAI, Anthropic, DeepSeek, and Mistral, etc.
- **Three Agent Modes**:
  - **cmd** — One-shot shell command generation with optional execution
  - **qa** — Question answering with single-turn or interactive multi-turn chat
  - **devops** — Autonomous reason–act–observe loop for multi-step sysadmin & ops tasks
- **Interactive Command Execution**: Safe command generation with user confirmation
- **Multi-turn Chat UI**: Full bubbletea TUI for interactive QA sessions (scrollable, word-wrapped)
- **Context-Aware**: Automatically detects OS, shell, working directory, and timestamp for better LLM responses
- **Flexible Configuration**: Environment variables or YAML config for easy setup

## 📦 Installation

### Prerequisites
- Go 1.26 or later
- API key for at least one supported LLM provider

### Build from Source
```bash
git clone https://github.com/yourusername/pai.git  # Replace with actual repo URL
cd pai
go build -o pai ./cmd/pai
# Optional: Add to PATH
mv ./pai ~/.local/bin/


# Or by go install
go install github.com/Carudy/pai/cmd/pai@latest
```


## ⚙️ Configuration

PAI supports configuration via environment variables or a config file.

### Environment Variables
```bash
export OPENAI_API_KEY="your-openai-key"
```

### Config File
Create `~/.config/pai/config.yml`:
```yaml
api_keys:
  deepseek: "your-deepseek-key"
  mistral: "your-mistral-key"

default_model: "deepseek:deepseek-chat"
default_agent: "devops"   # can be "cmd", "qa", or "devops"
streaming: true           # token-by-token streaming (default: false)
reasoning: true           # enable thinking/reasoning mode (default: false)

# Optional: override default prompts per agent
# **Currently not suggested for "devops", for may corrupt built-in logic**
prompts:
  qa: |
    You are a helpful assistant. Answer the user's question directly.
    Remember you are in a terminal environment. Use plain text only.
    Respond concisely and as short as possible.

  cmd: |
    You are a shell command generator. Rules:
    1. Generate one-line shell command(s) and a brief explanation based on the user's request.
    2. Output ONLY valid JSON: {"cmd": "your_shell_command", "comment": "brief explanation"}
    3. No markdown, no backticks, no extra text.

  devops: |
    You are a senior DevOps engineer running inside a terminal...
    (See internal/agent/prompt.go for the full default)
```

> **Note**: Model format is `provider:model_name` (e.g. `deepseek:deepseek-chat`, `openai:gpt-4o`, `anthropic:claude-sonnet-4-20250514`).

## 📖 Usage

### Command Generation (Default Agent)
Generate and optionally execute shell commands:
```bash
# Basic usage
pai "list all go files in this proj"
pai "<whatever-your-target>"

# Quotes aren't needed in most cases
# but for beautiful and not mis-understanding by shell we recommend using quotes
# below will generate "find . -name '*.go' | nl"-like command
pai -a cmd list all .go files recursively and numbering them

pai --agent cmd "sum numbers in column 3 of data.csv"
# "awk -F',' '{sum += $3} END {print sum}' data.csv"

# Enable debug mode
pai --debug "find large files in current directory"
```

### Example — CMD Agent
See examples/

## 🛣️ Todo
- [x] User-friendly TUI/CLI packages
- [x] Multi-turn chat in QA mode
- [x] DevOps agent for multi-turn tasks
- [ ] MCP, tools integration
- [ ] Local database for sessions, memory, etc.

## Uniqe features
- [ ] DevOps enhancements (e.g., learning and invoking CLI tools with RAG/fuzzy search)
- [ ] Dynamic agent-prompt loading 

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- [charmbracelet](https://github.com/charmbracelet) 
  + bubbletea: Terminal UI framework
  + Lipgloss: Style definitions for terminal output
