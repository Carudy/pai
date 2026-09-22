package chat

import "strings"

// ActionType is the action discriminator in every agent response. "tool" names
// its tool at the top level; the rest carry a plain string payload.
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

// OutputGuide is the system-owned output contract, rendered once into the head —
// after the tool specs and before the conversation. It is deliberately verbose:
// a concrete example per action is followed far more reliably than a described
// shape. The head is frozen and prefix-cached, so its length is paid once per
// session; the per-turn nudge is OutputReminder.
func OutputGuide() string {
	return `## Response format

Reply with exactly ONE JSON object and nothing else — no prose before or after
it, no markdown code fences. Every response has three fields:
  "action"  one of ` + ActionEnum() + `
  "payload" the action's content
  "reason"  a short explanation of why you chose this action

One example per action:

- tool — run a tool. "toolname" sits at the top level beside "payload"; payload
  holds the tool's arguments (an object when the tool's schema says so, else a
  string). Each tool's schema is listed above.
  ` + ToolExample() + `

- ask — ask the user a question; payload is the question string.
  {"action":"ask","payload":"Which host should I deploy to?","reason":"the target host is unclear"}

- done — the task is finished; payload is a summary of what you accomplished.
  {"action":"done","payload":"Deployed v1.2 to staging; the health check returned 200.","reason":"task complete"}

- terminate — the task cannot be completed; payload explains why.
  {"action":"terminate","payload":"No SSH access to the host and no credentials were provided.","reason":"cannot proceed"}

Every { needs a matching }; never emit more than one object.`
}

// OutputReminder is the per-turn one-liner rendered after the conversation — the
// last thing the model reads before generating. The full contract lives in the
// head; this only jogs it, and stays short so it costs almost nothing per turn.
func OutputReminder() string {
	return "Remember: reply with exactly one valid JSON object, no prose and no code fences."
}

// ToolExample is a literal tool response, embedded in the guide so the one shape
// with a nested object is shown concretely rather than described.
func ToolExample() string {
	return `{"action":"tool","toolname":"execute","payload":"df -h","reason":"check disk space"}`
}
