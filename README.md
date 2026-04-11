# aicmd

[![Go Version](https://img.shields.io/badge/go-1.25+-blue.svg)](https://golang.org)
[![License](https://img.shields.io/badge/license-Apache%202.0-green.svg)](LICENSE)

An intelligent CLI assistant that leverages large language models to generate shell commands or answer questions. Built with a modular architecture supporting multiple LLM providers and extensible through plugins.

## Features

- **Multi-Provider LLM Support**: Works with OpenAI, Anthropic, DeepSeek, and other popular LLM APIs through a unified interface
- **Dual Modes**:
  - Command Generation: Generate shell commands based on natural language descriptions
  - Q&A Mode: Ask general questions and get AI-powered responses
- **Interactive TUI**: Beautiful terminal user interface built with Bubbletea
- **YAML Configuration**: Flexible configuration system for API keys, models, and prompts
- **Modular Architecture**: Clean separation of concerns with extensible plugin system
- **Cross-Platform**: Supports Linux, macOS, and Windows
- **Extensible**: Plugin interface for adding custom skills, tools, and MCPs

## Installation

### Prerequisites

- Go 1.25 or later
- API keys for your chosen LLM provider(s)

### Build from Source

```bash
git clone https://github.com/yourusername/aicmd.git
cd aicmd
go build -o aicmd .
```

### Install Globally

```bash
go install github.com/yourusername/aicmd@latest
```

## Configuration

Create a configuration file at `~/.config/aicmd/config.yml`:

```yaml
provider: "openai"  # or "anthropic", "deepseek", etc.
api_keys:
  openai: "your-openai-api-key-here"
  anthropic: "your-anthropic-api-key-here"
default_model: "gpt-4o-mini"
ask_prompt: "You are a helpful assistant. Answer the user's question directly."
cmd_prompt: |
  You are a shell command generator for {{OS}}. Rules:
  1. Output ONLY valid JSON: {"cmd": "your_shell_command", "comment": "brief explanation"}
  2. No markdown, no backticks, no extra text.
```

### Environment Variables (Alternative)

You can also set API keys via environment variables:

```bash
export OPENAI_API_KEY="your-key-here"
export ANTHROPIC_API_KEY="your-key-here"
```

## Usage

### Command Generation (Default Mode)

Generate shell commands from natural language descriptions:

```bash
aicmd list all files in current directory recursively
aicmd find and delete all .log files older than 7 days
```

### Q&A Mode

Ask general questions:

```bash
aicmd --ask what is the capital of France
aicmd --ask explain how recursion works in programming
```

### Interactive Interface

The tool launches an interactive terminal UI where you can:
- Type your request
- See the AI-generated response
- For commands: review and confirm execution
- For questions: view the answer directly

## Examples

### Generate Commands

```bash
$ aicmd create a new directory called 'project' and navigate into it

💡 Create a new directory and change to it
> mkdir project && cd project

Execute? [y/N]: y
Command executed successfully.
```

### Ask Questions

```bash
$ aicmd --ask what are the benefits of using Go programming language

[AI Response appears in interactive interface]
```

## Supported Providers

- **OpenAI** (GPT-4, GPT-3.5-turbo, etc.)
- **Anthropic** (Claude models)
- **DeepSeek**
- **Mistral**
- **Ollama** (local models)
- And more through the any-llm-go library

## Architecture

The project is organized into modular packages:

- **`config/`**: Configuration loading and management
- **`llm/`**: LLM provider abstraction using any-llm-go
- **`cli/`**: Terminal user interface with Bubbletea
- **`cmdgen/`**: Prompt building and response parsing
- **`ext/`**: Extensibility framework for plugins and tools

## Extending aicmd

### Adding Custom Extensions

Implement the `Extension` interface:

```go
type MyExtension struct{}

func (e MyExtension) Name() string { return "my-extension" }
func (e MyExtension) Description() string { return "Custom functionality" }
func (e MyExtension) Execute(ctx context.Context, input string) (string, error) {
    // Your logic here
    return "result", nil
}
```

Register in your code:

```go
registry := ext.NewRegistry()
registry.Register(MyExtension{})
```

## Contributing

1. Fork the repository
2. Create a feature branch: `git checkout -b feature-name`
3. Make your changes and add tests
4. Run `go test ./...` and `go vet ./...`
5. Submit a pull request

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.

## Acknowledgments

- [any-llm-go](https://github.com/mozilla-ai/any-llm-go) for unified LLM API support
- [Bubbletea](https://github.com/charmbracelet/bubbletea) for the terminal UI framework
- [Mozilla AI](https://github.com/mozilla-ai) for the any-llm ecosystem</content>
<parameter name="filePath">README.md