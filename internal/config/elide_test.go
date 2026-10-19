package config

import "testing"

// The elision trigger is derived so a session that declares a budget trims before it
// would be summarized, and one that declares nothing still has a ceiling rather than
// growing without bound.
func TestElideLimitDerivation(t *testing.T) {
	cases := []struct {
		name string
		c    ContextConfig
		want int
	}{
		{"an explicit trigger wins", ContextConfig{ElideAfterTokens: 5, SummarizeAfterTokens: 100}, 5},
		{"otherwise half the summarize budget", ContextConfig{SummarizeAfterTokens: 100}, 50},
		{"otherwise the default ceiling", ContextConfig{}, DefaultElideAfterTokens},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.ElideLimit(); got != tc.want {
				t.Errorf("ElideLimit() = %d, want %d", got, tc.want)
			}
		})
	}
}
