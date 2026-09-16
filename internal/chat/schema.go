package chat

import "strings"

// ActionType is the action discriminator in every agent response. "tool"
// carries a structured {toolname, payload}; the rest carry a plain string
// payload.
type ActionType string

// The canonical, ordered action set. This is the single source of truth: both
// response validation and the output guide rendered into prompts derive from it.
const (
	ActionTool      ActionType = "tool"
	ActionAsk       ActionType = "ask"
	ActionDone      ActionType = "done"
	ActionTerminate ActionType = "terminate"
)

var actionOrder = []ActionType{ActionTool, ActionAsk, ActionDone, ActionTerminate}

func isValidAction(a ActionType) bool {
	for _, x := range actionOrder {
		if x == a {
			return true
		}
	}
	return false
}

// ActionEnum renders the action set as "tool|ask|done|terminate".
func ActionEnum() string {
	names := make([]string, len(actionOrder))
	for i, a := range actionOrder {
		names[i] = string(a)
	}
	return strings.Join(names, "|")
}

// OutputGuide is the system-owned output contract. It is rendered after the
// chat history on every request so the model is always reminded of the exact
// response format right before it generates. Keep it compact — it is repeated
// every turn.
func OutputGuide() string {
	return `Respond ONLY with a single JSON object — no prose, no markdown fences.
{"action":"` + ActionEnum() + `","payload":<payload>,"reason":"<short explanation>"}
payload by action:
- tool:      {"toolname":"<name>","payload":<tool arguments>}
- ask:       a string question for the user
- done:      a string summary of what was accomplished
- terminate: a string explaining why the task cannot be completed`
}
