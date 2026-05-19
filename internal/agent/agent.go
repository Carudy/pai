// Package agent provides LLM-powered agents (cmd, qa, devops) for PAI.
package agent

import (
	"context"

	"github.com/Carudy/pai/internal/config"
)

// Agent is the interface that all PAI agents implement.
type Agent interface {
	// Name returns the short identifier ("cmd", "qa", "devops").
	Name() string
	// Description returns a one-line summary of what the agent does.
	Description() string
	// Run executes the agent with the given user input.
	Run(ctx context.Context, cfg *config.UserConfig, userInput string) error
}

// registry holds all registered agents, keyed by name.
var registry = map[string]Agent{}

// Register adds an agent instance to the global registry.
// Panics on duplicate names.
func Register(a Agent) {
	if _, ok := registry[a.Name()]; ok {
		panic("agent: duplicate registration: " + a.Name())
	}
	registry[a.Name()] = a
}

// Get returns the agent registered under name, or nil if none.
func Get(name string) Agent {
	return registry[name]
}

// List returns all registered agents in insertion order.
func List() []Agent {
	agents := make([]Agent, 0, len(registry))
	for _, a := range registry {
		agents = append(agents, a)
	}
	return agents
}
