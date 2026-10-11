package config

import (
	"testing"

	"github.com/BurntSushi/toml"
)

// A provider's non-secret transport settings — base_url, protocol, and the
// optional headers table — must survive parsing into ProviderConfig.
func TestProviderTransportSettings(t *testing.T) {
	body := `
[providers.opencode]
base_url = "https://opencode.ai/zen/go/v1/responses"
api_key = "oc-secret"
protocol = "responses"

[providers.opencode.headers]
"User-Agent" = "pai/9"
"x-opencode-session" = "pinned"
`
	var raw tomlConfig
	if err := toml.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.fromTOML(&raw)

	pc, ok := cfg.ProvidersConfigs["opencode"]
	if !ok {
		t.Fatal("provider not parsed")
	}
	if pc.Protocol != "responses" {
		t.Errorf("protocol = %q, want responses", pc.Protocol)
	}
	if pc.BaseURL != "https://opencode.ai/zen/go/v1/responses" {
		t.Errorf("base_url = %q", pc.BaseURL)
	}
	if pc.Headers["User-Agent"] != "pai/9" || pc.Headers["x-opencode-session"] != "pinned" {
		t.Errorf("headers = %v", pc.Headers)
	}
}
