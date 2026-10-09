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
