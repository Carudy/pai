package core

// Session modes. A chat session is driven by instructions; a service session
// is resident and driven by the scheduler. The value is persisted, so it is
// shared vocabulary rather than a package-private detail.
const (
	ModeChat    = "chat"
	ModeService = "service"
)
