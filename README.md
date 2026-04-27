# PAI (Personal Agent Inside Terminal)

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.21+-blue.svg)](https://golang.org/)

An ultra-lightweight, module-decoupled, highly customizable CLI tool that leverages LLMs to assist with terminal tasks. PAI acts as your personal agent, helping you generate shell commands and answer questions directly in the terminal.

## ✨ Features

- **Multi-Provider LLM Support**: Compatible with OpenAI, Anthropic, and DeepSeek
- **Interactive Command Execution**: Safe command generation with user confirmation
- **Cross-Platform**: Works on macOS, Linux, and Windows with OS/shell-aware context
- **Rich Terminal UI**: Colored output and user-friendly interfaces powered by Bubbletea and Lipgloss
- **Flexible Configuration**: Environment variables or YAML config for easy setup
- **Extensible Architecture**: Modular design for future enhancements


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
```

## ⚙️ Configuration

PAI supports configuration via environment variables or a config file.

### Environment Variables
```bash
export OPENAI_API_KEY="your-openai-key"
export ANTHROPIC_API_KEY="your-anthropic-key"
export DEEPSEEK_API_KEY="your-deepseek-key"
```

### Config File
Create `~/.config/pai/config.yml`:
```yaml
api_keys:
  deepseek: "your-deepseek-key"
  openai: "your-openai-key"
  anthropic: "your-anthropic-key"

provider: "deepseek"
default_model: "deepseek-chat"

ask_prompt: |
  You are a helpful assistant. Answer the user's question directly.
  Remember you are in a terminal environment. Use plain text only.
  Respond concisely and as short as possible.

cmd_prompt: |
  You are a shell command generator. Rules:
  1. Generate one-line shell command(s) and a brief explanation based on the user's request.
  2. Output ONLY valid JSON: {"cmd": "your_shell_command", "comment": "brief explanation"}
  3. No markdown, no backticks, no extra text.
```

> **Note**: Currently supports ask-answer and command generation agents.

## 📖 Usage

### Command Generation (Default Action)
Generate and optionally execute shell commands:
```bash
# Basic usage
pai "list all .go files recursively"

# Actually no "" is ok
pai list all .go files recursively and numbering them
# will generate "find . -name '*.go' | nl"-like cmd

pai --action cmd sum numbers in column 3 of data.csv
# "awk -F',' '{sum += $3} END {print sum}' data.csv"

# Enable debug mode
pai --debug "find large files in current directory"
```

### Full example
```bash
> pai generate randomly generated 3x3 numbers and write to data.csv
🤖 Processing...
💡 Comment:
        Generates a 3x3 grid of random integers (0-9) and writes it to data.csv as comma-separated values without newline at end.
💻 Command:
        python3 -c "import csv,random; d=[[random.randint(0,9) for _ in range(3)] for _ in range(3)]; open('data.csv','w').write('\n'.join([','.join(map(str,r)) for r in d]))"
Execute the command ?
[*] Yes
[ ] No
  (Press q or ctrl+c to quit.)
Executed successfully.

> bat data.csv
─────┬────────────────────────────────────────────────────────────────────────────────────────────────────────
     │ File: data.csv
─────┼────────────────────────────────────────────────────────────────────────────────────────────────────────
   1 │ 4,9,1
   2 │ 8,4,1
   3 │ 1,1,3
─────┴────────────────────────────────────────────────────────────────────────────────────────────────────────
> pai sum numbers in 2nd column of data.csv
🤖 Processing...
💡 Comment:
        Calculates the sum of values in the second column of a comma-separated file.
💻 Command:
        awk -F',' '{sum+=$2} END {print sum}' data.csv
Execute the command ?
[*] Yes
[ ] No

  (Press q or ctrl+c to quit.)
Executed successfully.
14
```


### Question Answering
Get direct answers to your questions:
```bash
pai --action qa "what is recursion"
pai -a qa "explain Go goroutines in one sentence"
```


## 🛣️ Todo
- [x] User-friendly TUI/CLI packages
- [ ] Multi-turn chat/tasks support (ask-ans done)
- [ ] MCP, tools, and skills integration
- [ ] Daemon mode for persistent sessions/quick start
- [ ] Local database for memory management
- [ ] DevOps enhancements (e.g., learning and invoking CLI tools with RAG/fuzzy search)


## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- [any-llm-go](https://github.com/mozilla-ai/any-llm-go) - Multi-provider LLM client
- [Bubbletea](https://github.com/charmbracelet/bubbletea) - Terminal UI framework
- [Lipgloss](https://github.com/charmbracelet/lipgloss) - Style definitions for terminal output
