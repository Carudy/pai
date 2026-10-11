package provider

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// BuiltinProviders are the providers PAI knows a default endpoint for. Any other
// name still works, as long as base_url is set.
var BuiltinProviders = []string{"deepseek", "mistral", "openai"}

// defaultUserAgent identifies PAI on the wire. Providers such as the OpenCode Go
// gateway ask clients to send their own agent name rather than a generic SDK or
// HTTP-library default, which is what Go's transport would otherwise use.
const defaultUserAgent = "pai"

// openCodeSessionHeader is the routing/cache-affinity session id the OpenCode Go
// gateway requires; a request without it is rejected outright. See
// https://opencode.ai/docs/go/#where-can-i-use-it.
const openCodeSessionHeader = "x-opencode-session"

// isSessionHeaderHost reports whether endpoint is the OpenCode Go gateway, the
// one host that requires the routing session header.
func isSessionHeaderHost(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "opencode.ai" || strings.HasSuffix(host, ".opencode.ai")
}

// Protocols select the wire format a provider speaks.
const (
	// ProtocolChat is the OpenAI-compatible chat completions API: POST to
	// /chat/completions and read `data:` framed SSE deltas. This is the default.
	ProtocolChat = "chat"
	// ProtocolResponses is OpenAI's Responses API: POST to /responses, read named
	// SSE events. It is what Codex-style clients use, and it is the only one that
	// returns reasoning as a model-written summary.
	ProtocolResponses = "responses"
)

// ProviderConfig selects and configures a transport. Callers pass the settings
// from config.toml; this package never reads configuration itself.
type ProviderConfig struct {
	Name     string // provider name, e.g. "openai"; used for defaults and errors
	APIKey   string
	Model    string
	BaseURL  string // complete endpoint URL; empty means the built-in default
	Protocol string // "" or ProtocolChat; ProtocolResponses for /responses
	// Headers are extra request headers sent on every call, from the
	// [providers.<name>.headers] table in config.toml. They win over PAI's own
	// defaults (User-Agent, and the OpenCode routing session id).
	Headers map[string]string
}

// providerSpec describes what makes a provider unique.
type providerSpec struct {
	name    string // human-readable label for error messages
	baseURL string // default chat completions endpoint
	// responsesURL is the default /responses endpoint, empty when the provider has
	// no such API. A configured base_url overrides either one.
	responsesURL string
	// headers are the fully-resolved request headers, including PAI's defaults.
	headers map[string]string
	// bodyEnricher optionally adds provider-specific request fields.
	bodyEnricher func(body map[string]any, reasoning bool)
}

// CreateClient builds the transport for cfg. An unknown protocol is rejected
// here rather than surfacing later as a confusing request failure.
func CreateClient(cfg ProviderConfig) (Provider, error) {
	protocol := cfg.Protocol
	if protocol == "" {
		protocol = ProtocolChat
	}
	spec, known := builtInProviders[cfg.Name]

	switch protocol {
	case ProtocolChat:
		if !known {
			// Unknown provider: a generic OpenAI-compatible endpoint. baseURL is
			// the complete URL, e.g. https://api.example.com/v1/chat/completions.
			if cfg.BaseURL == "" {
				return nil, fmt.Errorf("unsupported provider %q and no base_url configured", cfg.Name)
			}
			spec = providerSpec{name: cfg.Name, baseURL: cfg.BaseURL}
		} else if cfg.BaseURL != "" {
			spec.baseURL = cfg.BaseURL
		}
		spec.headers = resolveHeaders(cfg.Headers, spec.baseURL)
		return newOpenAIProvider(cfg.APIKey, cfg.Model, spec), nil

	case ProtocolResponses:
		endpoint := cfg.BaseURL
		if endpoint == "" {
			endpoint = spec.responsesURL
		}
		if endpoint == "" {
			return nil, fmt.Errorf("provider %q has no built-in %s endpoint; set base_url", cfg.Name, ProtocolResponses)
		}
		spec = providerSpec{name: cfg.Name, baseURL: endpoint}
		spec.headers = resolveHeaders(cfg.Headers, endpoint)
		return newResponsesProvider(cfg.APIKey, cfg.Model, spec), nil

	default:
		return nil, fmt.Errorf("unknown protocol %q for provider %q (want %q or %q)",
			cfg.Protocol, cfg.Name, ProtocolChat, ProtocolResponses)
	}
}

// resolveHeaders merges the user's configured headers with PAI's defaults: a
// User-Agent identifying PAI, and (for the OpenCode Go gateway) a routing
// session id. The user's own keys always win, matched case-insensitively so an
// explicit override is honoured regardless of how it is capitalised.
func resolveHeaders(user map[string]string, endpoint string) map[string]string {
	headers := make(map[string]string, len(user)+2)
	for k, v := range user {
		headers[k] = v
	}
	if !hasHeader(headers, "User-Agent") {
		headers["User-Agent"] = defaultUserAgent
	}
	if isSessionHeaderHost(endpoint) && !hasHeader(headers, openCodeSessionHeader) {
		headers[openCodeSessionHeader] = newSessionID()
	}
	return headers
}

// hasHeader reports whether headers already names key, ignoring capitalisation.
func hasHeader(headers map[string]string, key string) bool {
	for k := range headers {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// newSessionID returns a random routing session id. It is stable for the life of
// one client (one run or one hosted session), which is what the gateway needs to
// route and cache prompts; it is not persisted across runs.
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read failing is practically impossible; a timestamp still yields a
		// usable, if less unique, id rather than failing the whole request.
		return fmt.Sprintf("pai-%d", time.Now().UnixNano())
	}
	return "pai-" + hex.EncodeToString(b[:])
}
