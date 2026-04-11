package userconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"pai/internal/configs"
)

type Config struct {
	Provider     string            `yaml:"provider"`
	APIKeys      map[string]string `yaml:"api_keys"`
	DefaultModel string            `yaml:"default_model"`
	AskPrompt    string            `yaml:"ask_prompt"`
	CmdPrompt    string            `yaml:"cmd_prompt"`
}

func LoadConfig() (*Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home dir: %w", err)
	}

	var config Config
	configPath := filepath.Join(homeDir, ".config", "pai", "config.yml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		// Return default config if file not found
		config = Config{
			Provider:     "deepseek",
			APIKeys:      make(map[string]string),
			DefaultModel: "deepseek-chat",
			AskPrompt:    "You are a helpful assistant. Answer the user's question directly.",
			CmdPrompt:    "You are a shell command generator for {{OS}}. Rules:\n1. Output ONLY valid JSON: {\"cmd\": \"your_shell_command\", \"comment\": \"brief explanation\"}\n2. No markdown, no backticks, no extra text.",
		}

		// fill APIKeys in env
		for _, key := range configs.Supported_providers {
			envKey := strings.ToUpper(key) + "_API_KEY"
			if val := os.Getenv(envKey); val != "" {
				config.APIKeys[key] = val
			}
		}

	} else {
		if err := yaml.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal config: %w", err)
		}
	}

	return &config, nil
}
