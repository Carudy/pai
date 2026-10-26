package role

// outcome is the terminal result of one activation (one bounded ReAct episode).
// A tool call is not terminal: it continues the activation. Only these outcomes
// hand control back to the driver, which decides whether to wait for the user
// (chat) or sleep until the next event (service).
type outcome uint8

const (
	// outcomeContinue means the step ran a tool and the activation continues.
	outcomeContinue outcome = iota
	// outcomeYield means the activation stops and the session sleeps. Service
	// mode uses it for both yield and inform.
	outcomeYield
	// outcomeDone means the current task is complete.
	outcomeDone
	// outcomeAsk means the model asked the human; the session parks.
	outcomeAsk
	// outcomeTerminate means the task cannot proceed.
	outcomeTerminate
)

// String names the outcome for logs and tests.
func (o outcome) String() string {
	switch o {
	case outcomeContinue:
		return "continue"
	case outcomeYield:
		return "yield"
	case outcomeDone:
		return "done"
	case outcomeAsk:
		return "ask"
	case outcomeTerminate:
		return "terminate"
	default:
		return "unknown"
	}
}

// activationResult carries the terminal outcome plus whether the step was
// interrupted by the user (Ctrl+C / abort). An interrupt is not a service
// outcome: chat waits for the next instruction, service nacks and sleeps.
// Status is the terminal action's payload, bounded for a session view; the full
// text is already in the transcript.
type activationResult struct {
	outcome     outcome
	status      string
	interrupted bool
}
