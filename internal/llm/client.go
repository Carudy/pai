package llm

import "fmt"

// ProviderConfig holds common configuration for creating a provider.
type ProviderConfig struct {
	APIKey    string
	Model     string
	BaseURL   string
	Reasoning bool
}

// NewDeepSeekProvider creates a new DeepSeek provider.
func NewDeepSeekProvider(cfg ProviderConfig) Provider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	return &deepSeekProvider{
		apiKey:    cfg.APIKey,
		model:     cfg.Model,
		baseURL:   baseURL,
		reasoning: cfg.Reasoning,
	}
}

// NewMistralProvider creates a new Mistral provider.
func NewMistralProvider(cfg ProviderConfig) Provider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.mistral.ai"
	}
	return &mistralProvider{
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		baseURL: baseURL,
	}
}

// NewOpenAIProvider creates a new OpenAI provider.
func NewOpenAIProvider(cfg ProviderConfig) Provider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	return &openaiProvider{
		apiKey:    cfg.APIKey,
		model:     cfg.Model,
		baseURL:   baseURL,
		reasoning: cfg.Reasoning,
	}
}

// CreateClient creates the appropriate Provider based on provider name.
func CreateClient(providerName string, apiKey string, model string, reasoning bool) (Provider, error) {
	switch providerName {
	case "openai":
		return NewOpenAIProvider(ProviderConfig{
			APIKey:    apiKey,
			Model:     model,
			Reasoning: reasoning,
		}), nil
	case "deepseek":
		return NewDeepSeekProvider(ProviderConfig{
			APIKey:    apiKey,
			Model:     model,
			Reasoning: reasoning,
		}), nil
	case "mistral":
		return NewMistralProvider(ProviderConfig{
			APIKey: apiKey,
			Model:  model,
		}), nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerName)
	}
}
