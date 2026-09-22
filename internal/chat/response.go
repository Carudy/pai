package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/provider"
)

const maxFormatRetries = 3

// Response is one agent response: a single JSON object with an action, a payload,
// and a short reason.
type Response struct {
	Action ActionType `json:"action"`
	// ToolName names the tool to run, for the "tool" action. It sits at the top
	// level beside "payload" rather than nested inside it: "tool" was the only
	// multi-level shape, and models miscounted its braces.
	ToolName string          `json:"toolname,omitempty"`
	Payload  json.RawMessage `json:"payload"`
	Reason   string          `json:"reason"`
}

// GetPayload decodes the JSON-encoded payload string into a plain Go string.
func (r *Response) GetPayload() string {
	var s string
	if err := json.Unmarshal(r.Payload, &s); err == nil {
		return strings.TrimRight(strings.TrimSpace(s), "\n")
	}

	var v any
	if err := json.Unmarshal(r.Payload, &v); err != nil {
		return strings.TrimRight(string(r.Payload), "\n")
	}

	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return strings.TrimRight(string(r.Payload), "\n")
	}

	return string(b)
}

// Validate checks the response conforms to the agent schema.
func (r *Response) Validate() error {
	if !isValidAction(r.Action) {
		valid := ActionEnum()
		if r.Action == "" {
			return fmt.Errorf(`missing "action" field; must be one of: %s`, valid)
		}
		return fmt.Errorf(`invalid action %q; must be one of: %s`, r.Action, valid)
	}
	p := strings.TrimSpace(string(r.Payload))
	if p == "" || p == "null" || p == `""` {
		return fmt.Errorf(`"payload" must not be empty for action %q`, r.Action)
	}

	// "tool" must name the tool to run, at the top level.
	if r.Action == ActionTool && r.ToolName == "" {
		return fmt.Errorf(`"tool" needs a top-level "toolname", e.g. %s`, ToolExample())
	}

	return nil
}

// ParseResponse extracts and decodes the agent response from raw model output.
//
// Models wrap the object in prose or emit stray braces, so we scan for
// brace-balanced candidates and take the first that is a *recognized* action;
// failing that, the first that decodes at all — field-level complaints stay with
// Validate, which is where the retry loop expects them.
func ParseResponse(content string) (*Response, error) {
	candidates := jsonCandidates(content)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no complete JSON object in AI response (unbalanced braces, likely truncated)")
	}

	var (
		fallback *Response
		lastErr  error
	)
	for _, c := range candidates {
		var resp Response
		if err := json.Unmarshal([]byte(c), &resp); err != nil {
			lastErr = err
			continue
		}
		if isValidAction(resp.Action) {
			return &resp, nil
		}
		if fallback == nil {
			fallback = &resp
			lastErr = nil
		}
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, fmt.Errorf("failed to parse agent JSON: %w\nraw: %s", lastErr, candidates[0])
}

// ParseResponseWithRetry parses and validates a Response from the AI's output.
// On failure it feeds a descriptive correction message back to the AI and
// retries up to maxFormatRetries times before giving up.
func ParseResponseWithRetry(
	ctx context.Context,
	cfg *config.UserConfig,
	rp *RolePrompt,
	p Ports,
	content string,
	history []provider.Message,
) (*Response, []provider.Message, error) {
	var (
		resp *Response
		err  error
	)
	for attempt := 0; attempt < maxFormatRetries; attempt++ {
		resp, err = ParseResponse(content)
		if err == nil {
			err = resp.Validate()
		}
		if err == nil {
			return resp, history, nil
		}

		p.Logger.Debugf("[Format Error attempt %d/%d]: %v\n", attempt+1, maxFormatRetries, err)

		if attempt < maxFormatRetries-1 {
			p.Observer.Notice(fmt.Sprintf("Response format error, retrying (%d/%d): %v", attempt+1, maxFormatRetries, err))

			correctionMsg := fmt.Sprintf(`[system] Your previous response was not valid JSON: %v
Reply with exactly ONE complete JSON object — every { needs a matching }, no prose and no code fences.
A tool action looks like:
%s
The other actions take a string payload: {"action":"done|ask|terminate","payload":"<string>","reason":"<short>"}`,
				err, ToolExample())
			history = append(history, provider.Message{Role: provider.RoleUser, Content: correctionMsg})

			content, history, _, err = ChatStr(ctx, cfg, rp, p, history)
			if err != nil {
				return nil, history, err
			}
			p.Logger.Debugf("[AI Retry Output]:\n%s\n", content)
		}
	}
	return nil, history, fmt.Errorf("AI failed to produce valid JSON after %d attempts: %w", maxFormatRetries, err)
}
