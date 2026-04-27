package llm

import (
	"fmt"

	anyllm "github.com/mozilla-ai/any-llm-go"
	"github.com/mozilla-ai/any-llm-go/providers/anthropic"
	"github.com/mozilla-ai/any-llm-go/providers/deepseek"
	"github.com/mozilla-ai/any-llm-go/providers/mistral"
	"github.com/mozilla-ai/any-llm-go/providers/openai"

	"pai/internal/config"
)

func CreateClient(cfg *config.UserConfig) (anyllm.Provider, error) {
	apiKey, ok := cfg.APIKeys[cfg.Provider]
	if !ok {
		return nil, fmt.Errorf("API key for provider %s not found", cfg.Provider)
	}

	switch cfg.Provider {
	case "openai":
		return openai.New(anyllm.WithAPIKey(apiKey))
	case "anthropic":
		return anthropic.New(anyllm.WithAPIKey(apiKey))
	case "deepseek":
		return deepseek.New(anyllm.WithAPIKey(apiKey))
	case "mistral":
		return mistral.New(anyllm.WithAPIKey(apiKey))
	default:
		return nil, fmt.Errorf("unsupported provider: %s", cfg.Provider)
	}
}
