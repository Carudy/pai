package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/llm"
	"github.com/Carudy/pai/internal/ui"
)

const MaxFormatRetries = 3

type ActionType string

// DevOps Actions
const (
	ActionExecute   ActionType = "execute"
	ActionAsk       ActionType = "ask"
	ActionInfo      ActionType = "info"
	ActionDone      ActionType = "done"
	ActionTerminate ActionType = "terminate"
)

type AgentResponse struct {
	Action  ActionType      `json:"action"`
	Payload json.RawMessage `json:"payload"`
	Reason  string          `json:"reason"`
}

// GetPayload decodes the JSON-encoded payload string into a plain Go string.
// This correctly handles escape sequences like \n, \t, etc.
// Falls back to the raw payload bytes if JSON decoding fails.
func (r *AgentResponse) GetPayload() (string, error) {
	var s string
	if err := json.Unmarshal(r.Payload, &s); err == nil {
		return s, nil
	}
	// Fallback: strip surrounding quotes and return raw
	return strings.Trim(string(r.Payload), `"`), nil
}

// validActions is the set of allowed action values (matches schema.md)
var validActions = map[ActionType]bool{
	ActionExecute:   true,
	ActionAsk:       true,
	ActionInfo:      true,
	ActionDone:      true,
	ActionTerminate: true,
}

// Validate checks the response conforms to the agent schema.
// Returns a descriptive error so it can be fed back to the AI for correction.
func (r *AgentResponse) Validate() error {
	if !validActions[r.Action] {
		valid := `"execute", "ask", "info", "done", "terminate"`
		if r.Action == "" {
			return fmt.Errorf(`missing "action" field; must be one of: %s`, valid)
		}
		return fmt.Errorf(`invalid action %q; must be one of: %s`, r.Action, valid)
	}
	p := strings.TrimSpace(string(r.Payload))
	if p == "" || p == "null" || p == `""` {
		return fmt.Errorf(`"payload" must not be empty for action %q`, r.Action)
	}
	return nil
}

func ParseAgentResponse(content string) (*AgentResponse, error) {
	json_str, err := ExtractJSON(content)
	if err != nil {
		return nil, err
	}

	var resp AgentResponse
	if err := json.Unmarshal([]byte(json_str), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse agent JSON: %w\nraw: %s", err, json_str)
	}
	return &resp, nil
}

// parseResponseWithRetry parses and validates an AgentResponse from the AI's
// output. On failure it feeds a descriptive correction message back to the AI
// and retries up to maxFormatRetries times before giving up.
func parseResponseWithRetry(
	ctx context.Context,
	cfg *config.UserConfig,
	provider llm.Provider,
	content string,
	history []llm.Message,
) (*AgentResponse, []llm.Message, error) {
	const maxFormatRetries = 3
	var (
		resp *AgentResponse
		err  error
	)
	for attempt := 0; attempt < maxFormatRetries; attempt++ {
		resp, err = ParseAgentResponse(content)
		if err == nil {
			err = resp.Validate()
		}
		if err == nil {
			return resp, history, nil
		}

		config.DebugLog(os.Stdout, "[Format Error attempt %d/%d]: %v\n", attempt+1, maxFormatRetries, err)
		if attempt == maxFormatRetries-1 {
			return nil, history, fmt.Errorf("AI failed to produce valid JSON after %d attempts: %w", maxFormatRetries, err)
		}

		fmt.Printf("%s \u26a0\ufe0f %s\n",
			ui.Styles["TagSystem"].Render("[SYS]"),
			ui.Styles["Warn"].Render(fmt.Sprintf("Response format error, retrying (%d/%d): %v", attempt+1, maxFormatRetries, err)))

		correctionMsg := fmt.Sprintf(
			"[system] Your previous response had a format error: %v\n"+
				`Respond ONLY with valid JSON: {"action": "execute|ask|info|done|terminate", "payload": "...", "reason": "..."}`,
			err)
		history = append(history, llm.Message{Role: llm.RoleUser, Content: correctionMsg})

		content, history, err = chatStr(ctx, cfg, provider, history)
		if err != nil {
			return nil, history, err
		}
		config.DebugLog(os.Stdout, "[AI Retry Output]:\n%s\n", content)
	}
	// unreachable, but satisfies the compiler
	return nil, history, fmt.Errorf("AI failed to produce valid JSON after %d attempts: %w", maxFormatRetries, err)
}
