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
- **Streaming Output**: Optional token-by-token streaming when supported by the LLM provider — set `streaming: true` in config
- **Reasoning/Thinking Mode**: Optional `reasoning: true` enables LLM thinking output — shown with `🤔` prefix during thinking, then a separator before the final response (works with or without streaming). For DeepSeek, injects the `thinking` toggle + `reasoning_effort: high` to explicitly enable thinking; for OpenAI, passes `reasoning_effort: high`.
- **Flexible Configuration**: Environment variables or YAML config for easy setup

## 📦 Installation

### Prerequisites
- Go 1.21 or later
- API key for at least one supported LLM provider

### Build from Source
```bash
git clone https://github.com/yourusername/pai.git  # Replace with actual repo URL
cd pai
go build -o pai ./cmd/pai

# Optional: Add to PATH
mv ./pai ~/.local/bin/
# or
mv ./pai ~/go/bin/
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
default_agent: "cmd"   # can be "cmd", "qa", or "devops"
streaming: true        # token-by-token streaming (default: false)
reasoning: true        # enable thinking/reasoning mode (default: false)

# Optional: override default prompts per agent
# Currently not suggested for "devops", for may corrupt built-in logic
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

### Command Generation (Default Action)
Generate and optionally execute shell commands:
```bash
# Basic usage
pai "list all .go files recursively"

# Quotes aren't needed in most cases
pai list all .go files recursively and numbering them
# Generates "find . -name '*.go' | nl"-like command

pai --action cmd sum numbers in column 3 of data.csv
# "awk -F',' '{sum += $3} END {print sum}' data.csv"

# Enable debug mode
pai --debug "find large files in current directory"
```

### Example — CMD Agent
```bash
> pai generate randomly generated 3x3 numbers and write to data.csv
[Sys] Generating command...
[Exec] Generates a 3x3 grid of random integers (0-9) and writes it to data.csv as comma-separated values without newline at end.
[Exec] python3 -c "import csv,random; d=[[random.randint(0,9) for _ in range(3)] for _ in range(3)]; open('data.csv','w').write('\n'.join([','.join(map(str,r)) for r in d]))"
Execute the command ?
[*] Yes
[ ] No
  (Press q or ctrl+c to quit.)
[Sys] Command succeeded
[Res] (output shown here)

> pai sum numbers in 2nd column of data.csv
[Sys] Generating command...
[Exec] Calculates the sum of values in the second column of a comma-separated file.
[Exec] awk -F',' '{sum+=$2} END {print sum}' data.csv
Execute the command ?
[*] Yes
[ ] No
  (Press q or ctrl+c to quit.)
[Sys] Command succeeded
[Res] 14
```

### Question Answering — QA Agent
Get direct answers to your questions, or start an interactive multi-turn session:
```bash
# Single turn
pai --action qa "what is recursion"
pai -a qa "explain Go goroutines in one sentence"

# Interactive multi-turn chat (full TUI with scrolling)
pai -a qa -i "help me understand Kubernetes pods"
# Or with the short flag:
pai -a qa -i
```

### DevOps Agent — Multi-step Autonomous Tasks
The devops agent runs a reason–act–observe loop — it can run commands, inspect output, ask you questions, and iterate until the goal is achieved:
```bash
pai -a devops "check disk usage and alert if any partition is above 80%"
pai -a devops "set up a new nginx reverse proxy for myapp on port 3000"
```

## 🛣️ Todo
- [x] User-friendly TUI/CLI packages
- [x] Multi-turn chat in QA mode
- [x] DevOps agent for multi-turn tasks
- [ ] MCP, tools, and skills integration
- [ ] Local database
- [ ] DevOps enhancements (e.g., learning and invoking CLI tools with RAG/fuzzy search)

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- [Bubbletea](https://github.com/charmbracelet/bubbletea) - Terminal UI framework
- [Lipgloss](https://github.com/charmbracelet/lipgloss) - Style definitions for terminal output
