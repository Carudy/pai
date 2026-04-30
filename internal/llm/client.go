package llm

import "fmt"

// ProviderConfig holds common configuration for creating a provider.
type ProviderConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// providerSpec describes what makes a provider unique.
type providerSpec struct {
	name         string                                    // human-readable label for error messages
	baseURL      string                                    // default base URL
	bodyEnricher func(body map[string]any, reasoning bool) // optional: add provider-specific body fields
}

// CreateClient creates the appropriate Provider based on provider name.
func CreateClient(providerName, apiKey, model, baseURL string) (Provider, error) {
	spec, ok := builtInProviders[providerName]
	if !ok {
		// Unknown provider: use generic OpenAI-compatible format.
		// baseURL is the complete endpoint URL (e.g. https://api.example.com/v1/chat/completions).
		if baseURL == "" {
			return nil, fmt.Errorf("unsupported provider %q and no base_url configured", providerName)
		}
		return newOpenAIProvider(apiKey, model, providerSpec{
			name:    providerName,
			baseURL: baseURL,
		}), nil
	}
	return newOpenAIProvider(apiKey, model, spec), nil
}
