package llm

import "fmt"

// ---------------------------------------------------------------------------
// Provider configuration
// ---------------------------------------------------------------------------

// ProviderConfig holds common configuration for creating a provider.
type ProviderConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// providerFactory creates a Provider from config.
type providerFactory func(cfg ProviderConfig) Provider

// ---------------------------------------------------------------------------
// Provider registry
// ---------------------------------------------------------------------------

var providers = map[string]providerFactory{}

// registerProvider registers a provider constructor under the given name.
// Called from init() in provider-specific files.
func registerProvider(name string, factory providerFactory) {
	providers[name] = factory
}

// ---------------------------------------------------------------------------
// Provider specs (defaults used by the generic OpenAI-compatible provider)
// ---------------------------------------------------------------------------

// providerSpec describes what makes a provider unique.
type providerSpec struct {
	name         string                                    // human-readable label for error messages
	baseURL      string                                    // default base URL
	apiPath      string                                    // path appended to baseURL
	hasReasoning bool                                      // does this provider support reasoning/thinking?
	bodyEnricher func(body map[string]any, reasoning bool) // optional: add provider-specific body fields
}

// ---------------------------------------------------------------------------
// Public constructors
// ---------------------------------------------------------------------------

// NewOpenAIProvider creates a new OpenAI provider.
func NewOpenAIProvider(cfg ProviderConfig) Provider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	spec := openaiSpec
	spec.baseURL = baseURL
	return newOpenAIProvider(cfg.APIKey, cfg.Model, spec)
}

// NewDeepSeekProvider creates a new DeepSeek provider.
func NewDeepSeekProvider(cfg ProviderConfig) Provider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	spec := deepSeekSpec
	spec.baseURL = baseURL
	return newOpenAIProvider(cfg.APIKey, cfg.Model, spec)
}

// NewMistralProvider creates a new Mistral provider.
func NewMistralProvider(cfg ProviderConfig) Provider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.mistral.ai"
	}
	spec := mistralSpec
	spec.baseURL = baseURL
	return newOpenAIProvider(cfg.APIKey, cfg.Model, spec)
}

// ---------------------------------------------------------------------------
// Registry-based factory
// ---------------------------------------------------------------------------

// CreateClient creates the appropriate Provider based on provider name.
func CreateClient(providerName, apiKey, model string) (Provider, error) {
	factory, ok := providers[providerName]
	if !ok {
		return nil, fmt.Errorf("unsupported provider: %s", providerName)
	}
	return factory(ProviderConfig{
		APIKey: apiKey,
		Model:  model,
	}), nil
}

// ---------------------------------------------------------------------------
// init: register built-in providers
// ---------------------------------------------------------------------------

func init() {
	registerProvider("openai", NewOpenAIProvider)
	registerProvider("deepseek", NewDeepSeekProvider)
	registerProvider("mistral", NewMistralProvider)
}
