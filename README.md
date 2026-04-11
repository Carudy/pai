# PAI (Personal Agent Interface)

A CLI tool that uses large language models to generate shell commands or answer questions from natural language input.

## Features

- Generate shell commands from descriptions
- Ask questions and get AI responses
- Supports multiple LLM providers (OpenAI, Anthropic, DeepSeek)
- Interactive command execution with user confirmation
- Cross-platform support with detailed OS/shell context
- Colored terminal output for better UX

## Installation

### Prerequisites
- Go 1.21+
- API key for an LLM provider

### Build from Source
```bash
git clone <repo-url>
cd pai
go build -o pai ./cmd/pai
```

## Configuration

Set API keys via environment variables:
```bash
export OPENAI_API_KEY="your-key"
export ANTHROPIC_API_KEY="your-key"
export DEEPSEEK_API_KEY="your-key"
```

Or create `~/.config/pai/config.yml`:
```yaml
provider: "deepseek"
api_keys:
  deepseek: "your-deepseek-key"
  openai: "your-openai-key"

default_model: "deepseek-chat"
ask_prompt: |
  You are a helpful assistant. Answer the user's question directly.
  Remember you are in a terminal environment. So don't response with markdown-like formatting.
  Use plain text that is directly readable only.
  And response concisely, and as short as possible.
cmd_prompt: |
  You are a shell command generator. Rules:
  1. According to the user's request, generate one-line shell command(s) and a brief explanation.
  2. Output ONLY valid JSON: {\"cmd\": \"your_shell_command\", \"comment\": \"brief explanation\"}
  3. No markdown, no backticks, no extra text.
```

## Usage

### Generate Commands
```bash
./pai "list all '.go' files recursively"
./pai "find and delete log files older than 7 days"
```

### Ask Questions
```bash
./pai --ask "what is recursion"
./pai --ask "explain Go goroutines"
```

### Command Execution
After generating a command, PAI prompts for execution:
```
💻 Command:
  rm -rf *.log

Execute? [y/N]:
```

# License
This project is available under the MIT License.

# Acknowledgments
- any-llm-go
