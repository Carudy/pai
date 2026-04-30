# PAI (Personal Agent Inside Terminal)

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.21+-blue.svg)](https://golang.org/)

An ultra-lightweight, module-decoupled, highly customizable CLI tool that leverages LLMs to assist with terminal tasks. PAI acts as your personal agent, helping you generate shell commands, answer questions, and perform multi-step DevOps workflows — all directly in the terminal.

## ✨ Current Features

- **Multi-Provider LLM Support**: Compatible with OpenAI, DeepSeek, and Mistral, etc.
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
reasoning: true           # enable thinking/reasoning mode (default: false)
```

> **Note**: Model format is `provider:model_name` (e.g. `deepseek:deepseek-chat`, `openai:gpt-4o`, `doubao:doubao-1-5-pro-32k`).

## 📖 Usage

**See examples/**

## 🛣️ Todo
- [x] User-friendly TUI/CLI packages
- [x] Multi-turn chat in QA mode
- [x] DevOps agent for multi-turn tasks
- [ ] MCP, tools integration
- [ ] Local database for sessions, memory, etc.

## Uniqe features
- [ ] DevOps enhancements (e.g., learning and invoking CLI tools with RAG/fuzzy search)
- [ ] Cross-server collabration 
- [ ] Dynamic agent-prompt loading 

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- [charmbracelet](https://github.com/charmbracelet) 
  + bubbletea: Terminal UI framework
  + Lipgloss: Style definitions for terminal output
