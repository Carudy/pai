package runner

import "github.com/Carudy/pai/internal/core"

// Snapshot.State values. Chat uses starting, busy, awaiting, and stopped; a
// resident service adds suspended (asleep, safe to evict) and parked (waiting
// for a human reply) in the next phase. Clients compare these strings, so keep
// them stable.
const (
	StateStarting  = "starting"
	StateBusy      = "busy"
	StateAwaiting  = "awaiting"
	StateSuspended = "suspended"
	StateParked    = "parked"
	StateStopped   = "stopped"
)

// Session modes, shared with the rest of the stack through core.
const (
	ModeChat    = core.ModeChat
	ModeService = core.ModeService
)
