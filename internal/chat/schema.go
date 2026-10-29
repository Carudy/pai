package chat

import "strings"

// ActionType is the action discriminator in every agent response. "tool" names
// its tool at the top level; the rest carry a plain string payload.
type ActionType string

// The canonical action set. Chat mode uses the first four; a service session
// adds the two capability-gated actions marked below. Response validation and
// the output guide both derive from an ActionSet, so a mode cannot advertise an
// action it would reject.
const (
	ActionTool      ActionType = "tool"
	ActionAsk       ActionType = "ask"
	ActionDone      ActionType = "done"
	ActionTerminate ActionType = "terminate"

	// ActionInform reports status, updates the status document, and ends the
	// activation. Service mode only.
	ActionInform ActionType = "inform"
	// ActionYield pauses until the next event. Service mode only.
	ActionYield ActionType = "yield"
)

// ActionSet is the ordered set of actions one mode allows. The order is the
// order in which the guide renders them.
type ActionSet []ActionType

// ChatActions is the interactive set. It is returned by value so a caller
// cannot mutate the canonical order.
func ChatActions() ActionSet {
	return ActionSet{ActionTool, ActionAsk, ActionDone, ActionTerminate}
}

// ServiceActions is the resident-session set: chat plus inform and yield.
func ServiceActions() ActionSet {
	return ActionSet{ActionTool, ActionAsk, ActionInform, ActionYield, ActionDone, ActionTerminate}
}

// Has reports whether the set allows a. The zero set means chat.
func (s ActionSet) Has(a ActionType) bool {
	for _, x := range s.orChat() {
		if x == a {
			return true
		}
	}
	return false
}

// Enum renders the set as "tool|ask|done|terminate". The zero set means chat.
func (s ActionSet) Enum() string {
	set := s.orChat()
	names := make([]string, len(set))
	for i, a := range set {
		names[i] = string(a)
	}
	return strings.Join(names, "|")
}

// orChat treats the zero value as chat, so a hand-built RolePrompt without an
// explicit action set keeps today's behaviour.
func (s ActionSet) orChat() ActionSet {
	if len(s) == 0 {
		return ChatActions()
	}
	return s
}

// knownActions is the parser's vocabulary. Parsing is syntactic: every action a
// mode could ever use is recognized, and capability is checked by ValidateFor.
var knownActions = []ActionType{
	ActionTool, ActionAsk, ActionInform, ActionYield, ActionDone, ActionTerminate,
}

func isKnownAction(a ActionType) bool {
	return ActionSet(knownActions).Has(a)
}

// ActionEnum renders the interactive action set. It is kept for callers that
// only care about the chat contract; new code uses ActionSet.Enum.
func ActionEnum() string {
	return ChatActions().Enum()
}

// OutputGuide is the system-owned output contract for chat mode.
func OutputGuide() string {
	return OutputGuideFor(ChatActions())
}

// OutputGuideFor renders the contract for the given action set. The head is
// frozen and prefix-cached, so its length is paid once per session; the
// per-turn nudge is OutputReminder.
func OutputGuideFor(actions ActionSet) string {
	set := actions.orChat()
	var b strings.Builder
	b.WriteString(`## Response format

Reply with exactly ONE JSON object and nothing else — no prose before or after
it, no markdown code fences. Every response has three fields:
  "action"  one of `)
	b.WriteString(set.Enum())
	b.WriteString(`
  "payload" the action's content
  "reason"  a short explanation of why you chose this action

Concrete examples per action:

`)
	for i, action := range set {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(actionGuide(action))
	}
	b.WriteString("\n\nEvery { needs a matching }; never emit more than one object.")
	return b.String()
}

// actionGuide is one action's section. The chat sections must stay byte-stable:
// they are part of the prefix-cached head.
func actionGuide(action ActionType) string {
	switch action {
	case ActionTool:
		return `- tool — run a tool. "toolname" sits at the top level beside "payload"; payload
  holds the tool's arguments, a string or an object per that tool's schema (listed
  above). For example, execute takes a plain command, or an object naming the
  directory to run it in:
  ` + ToolExample() + `
  ` + ToolObjectExample()
	case ActionAsk:
		return `- ask — ask the user a question; payload is the question string.
  {"action":"ask","payload":"Which host should I deploy to?","reason":"the target host is unclear"}`
	case ActionInform:
		return `- inform — report status to the user; payload is the status text. This ends the
  current activation, and the session sleeps until the next event.
  {"action":"inform","payload":"River level 3.8 m, above the warning band.","reason":"crossed the warning threshold"}`
	case ActionYield:
		return `- yield — nothing more to do right now; sleep until the next event. An optional
  object payload may carry hints ("until", "on"); unknown hints are ignored.
  {"action":"yield","payload":{"until":"2026-10-22T12:05:00Z","on":["watch:river-level","mail"]},"reason":"next sample is due at :05"}`
	case ActionDone:
		return `- done — the task is finished; payload is a summary of what you accomplished, use markdown format if suitable.
  {"action":"done","payload":"Deployed v1.2 to staging; the health check returned 200.","reason":"task complete"}`
	case ActionTerminate:
		return `- terminate — the task cannot be completed; payload explains why.
  {"action":"terminate","payload":"No SSH access to the host and no credentials were provided.","reason":"cannot proceed"}`
	default:
		return ""
	}
}

// OutputReminder is the per-turn one-liner rendered after the conversation — the
// last thing the model reads before generating. The full contract lives in the
// head; this only jogs it, and stays short so it costs almost nothing per turn.
func OutputReminder() string {
	return `Remember: reply with exactly one valid JSON object, no prose and no code fences; escape line breaks inside strings as \n.`
}

// ToolExample is a literal tool response with a plain string payload — the common
// case — embedded in the guide so the shape is shown rather than described.
func ToolExample() string {
	return `{"action":"tool","toolname":"execute","payload":"df -h","reason":"check disk space"}`
}

// ToolObjectExample is a second literal tool response, showing the object payload
// shape: a command plus the directory it runs in. "An object when the schema says
// so" alone did not stop the model from encoding a directory as `cd <dir> && …`,
// so the exact nested shape is shown beside the string one.
func ToolObjectExample() string {
	return `{"action":"tool","toolname":"execute","payload":{"cmd":"ls -la","dir":"/var/log"},"reason":"inspect the nginx log dir"}`
}
