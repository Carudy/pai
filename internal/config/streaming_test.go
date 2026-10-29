package config

import (
	"github.com/BurntSushi/toml"
	"testing"
)

func TestStreamingDefaultAndExplicitOverride(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"missing", "", true}, {"omitted", "[app]\ndefault_role = 'devops'", true},
		{"enabled", "[app]\nstreaming = true", true}, {"disabled", "[app]\nstreaming = false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw tomlConfig
			if err := toml.Unmarshal([]byte(tc.body), &raw); err != nil {
				t.Fatal(err)
			}
			cfg := defaultConfig()
			cfg.fromTOML(&raw)
			if cfg.Streaming != tc.want {
				t.Fatalf("streaming = %v, want %v", cfg.Streaming, tc.want)
			}
		})
	}
}

// max_output_tokens is opt-in: zero means the provider's default. A negative
// value is clamped rather than sent as an invalid request.
func TestMaxOutputTokens(t *testing.T) {
	for _, tc := range []struct {
		body string
		want int
	}{{"", 0}, {"[app]\nmax_output_tokens = 65536", 65536}, {"[app]\nmax_output_tokens = -1", 0}} {
		var raw tomlConfig
		if err := toml.Unmarshal([]byte(tc.body), &raw); err != nil {
			t.Fatal(err)
		}
		cfg := defaultConfig()
		cfg.fromTOML(&raw)
		if cfg.MaxOutputTokens != tc.want {
			t.Fatalf("%q: max_output_tokens = %d, want %d", tc.body, cfg.MaxOutputTokens, tc.want)
		}
	}
}
