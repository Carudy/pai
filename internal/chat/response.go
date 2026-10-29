package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/provider"
)

const maxFormatRetries = 3

// ErrTruncatedResponse means the model's output did not contain one complete
// brace-balanced object. The provider's finish reason is what distinguishes a
// token limit from a dropped stream or malformed output; this error only says
// the JSON itself is incomplete.
var ErrTruncatedResponse = errors.New("no complete JSON object in AI response (unbalanced braces, likely truncated)")

// logHead returns the first max runes of s, for a bounded debug line.
func logHead(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// logTail returns the last max runes of s, for a bounded debug line. It never
// splits a UTF-8 sequence.
func logTail(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return "…" + string(r[len(r)-max:])
}

// ReplyMeta is the provider metadata for the content being parsed. It is what
// turns "unbalanced braces" into an explainable diagnosis instead of a guess.
type ReplyMeta struct {
	FinishReason string
	Usage        *provider.Usage
}

// isIncompleteFinish reports provider outcomes where the content may be legitimately
// partial. A length limit is the common case; aborted and resource-interrupted
// responses are not normal stops either.
func isIncompleteFinish(reason string) bool {
	switch reason {
	case provider.FinishReasonLength, provider.FinishReasonMaxTokens, "max_output_tokens",
		provider.FinishReasonAborted, provider.FinishReasonFiltered,
		provider.FinishReasonIncomplete, "insufficient_system_resource":
		return true
	}
	return false
}

// finishReasonExplanation names what the provider said, without assuming it was
// a token limit. The distinction matters: a dropped terminal event, a content
// filter, and a short JSON error call for different fixes.
func finishReasonExplanation(reason string) string {
	switch reason {
	case provider.FinishReasonLength, provider.FinishReasonMaxTokens, "max_output_tokens":
		return "the provider hit its output token limit (finish_reason=" + reason + ")"
	case provider.FinishReasonIncomplete:
		return "the provider stream ended without a terminal event"
	case provider.FinishReasonAborted:
		return "the provider aborted generation (finish_reason=aborted)"
	case "insufficient_system_resource":
		return "the provider ran out of inference resources (finish_reason=insufficient_system_resource)"
	case provider.FinishReasonFiltered:
		return "the provider filtered the output (finish_reason=content_filter)"
	case provider.FinishReasonStop:
		return "the provider reported a normal stop, so the model emitted incomplete JSON"
	case "":
		return "the provider reported no finish_reason"
	default:
		return "the provider stopped for finish_reason=" + reason
	}
}

func incompleteNotice(meta ReplyMeta, attempt int, content string) string {
	tokens := ""
	if meta.Usage != nil && meta.Usage.CompletionTokens > 0 {
		tokens = fmt.Sprintf(", %d output tokens", meta.Usage.CompletionTokens)
		if meta.Usage.ReasoningTokens > 0 {
			tokens += fmt.Sprintf(" (%d reasoning)", meta.Usage.ReasoningTokens)
		}
	}
	notice := fmt.Sprintf("Model response was incomplete (%s; %d bytes%s); omitted it and retrying (%d/%d)",
		finishReasonExplanation(meta.FinishReason), len(content), tokens, attempt, maxFormatRetries)
	if tail := logTail(content, 240); tail != "" {
		notice += "; tail: " + fmt.Sprintf("%q", tail)
	}
	return notice
}

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

// Validate checks the response conforms to the chat schema.
func (r *Response) Validate() error {
	return r.ValidateFor(ChatActions())
}

// ValidateFor checks the response against a mode's action set. Parsing accepts
// every known action so a service reply is not mistaken for junk; this is the
// capability boundary that rejects an action the current mode does not allow.
func (r *Response) ValidateFor(actions ActionSet) error {
	set := actions.orChat()
	if !set.Has(r.Action) {
		valid := set.Enum()
		if r.Action == "" {
			return fmt.Errorf(`missing "action" field; must be one of: %s`, valid)
		}
		if isKnownAction(r.Action) {
			return fmt.Errorf(`action %q is not available in this mode; must be one of: %s`, r.Action, valid)
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

// repairJSONControls escapes raw control characters inside JSON string
// literals. Models routinely write a literal newline or tab inside a prose
// payload; that is invalid JSON, but it is unambiguous and preserving the text
// is better than retrying the whole request. Outside strings nothing changes.
func repairJSONControls(s string) (string, bool) {
	var b strings.Builder
	b.Grow(len(s))
	inStr, esc, changed := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inStr {
			b.WriteByte(c)
			if c == '"' {
				inStr = true
			}
			continue
		}
		if esc {
			b.WriteByte(c)
			esc = false
			continue
		}
		switch c {
		case '\\':
			b.WriteByte(c)
			esc = true
		case '"':
			b.WriteByte(c)
			inStr = false
		default:
			if c >= 0x20 {
				b.WriteByte(c)
				continue
			}
			changed = true
			switch c {
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			default:
				fmt.Fprintf(&b, `\u%04x`, c)
			}
		}
	}
	return b.String(), changed
}

// ParseResponse extracts and decodes the agent response from raw model output.
//
// Models wrap the object in prose or emit stray braces, so we scan for
// brace-balanced candidates and take the first that is a *recognized* action;
// failing that, the first that decodes at all — field-level complaints stay with
// Validate, which is where the retry loop expects them. A candidate that decodes
// only after escaping raw control characters is accepted too: a literal newline
// in a prose payload is a formatting mistake, not a reason to lose the answer.
func ParseResponse(content string) (*Response, error) {
	candidates := jsonCandidates(content)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w (%d bytes)", ErrTruncatedResponse, len(content))
	}

	var (
		fallback *Response
		lastErr  error
	)
	for _, c := range candidates {
		var resp Response
		err := json.Unmarshal([]byte(c), &resp)
		if err != nil {
			if fixed, changed := repairJSONControls(c); changed {
				if retryErr := json.Unmarshal([]byte(fixed), &resp); retryErr == nil {
					err = nil
				} else {
					lastErr = retryErr
					continue
				}
			}
			if err != nil {
				lastErr = err
				continue
			}
		}
		if isKnownAction(resp.Action) {
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
	return ParseResponseWithRetryMeta(ctx, cfg, rp, p, content, ReplyMeta{}, history)
}

// ParseResponseWithRetryMeta is ParseResponseWithRetry with the provider's finish
// reason and usage. Those fields are the difference between reporting the real
// cause of an incomplete response and guessing at a token limit.
func ParseResponseWithRetryMeta(
	ctx context.Context,
	cfg *config.UserConfig,
	rp *RolePrompt,
	p Ports,
	content string,
	meta ReplyMeta,
	history []provider.Message,
) (*Response, []provider.Message, error) {
	var (
		resp *Response
		err  error
	)
	for attempt := 0; attempt < maxFormatRetries; attempt++ {
		resp, err = ParseResponse(content)
		if err == nil {
			err = resp.ValidateFor(rp.actionSet())
		}
		if err == nil {
			return resp, history, nil
		}

		truncated := errors.Is(err, ErrTruncatedResponse) || isIncompleteFinish(meta.FinishReason)
		if p.Logger != nil {
			completion, reasoning := 0, 0
			if meta.Usage != nil {
				completion, reasoning = meta.Usage.CompletionTokens, meta.Usage.ReasoningTokens
			}
			p.Logger.Debugf("[Format Error attempt %d/%d] bytes=%d finish_reason=%q completion_tokens=%d reasoning_tokens=%d truncated=%v: %v\n",
				attempt+1, maxFormatRetries, len(content), meta.FinishReason, completion, reasoning, truncated, err)
			p.Logger.Debugf("[Format Error head]: %s\n", logHead(content, 400))
			p.Logger.Debugf("[Format Error tail]: %s\n", logTail(content, 400))
		}

		if attempt < maxFormatRetries-1 {
			if truncated {
				// A cut-off response is large and cannot be completed by editing it. Sending
				// it back makes the retry prompt bigger and slower, and invites the model to
				// continue the truncated text. Replace it with a short marker instead of
				// dropping it, so the conversation keeps alternating user/assistant turns.
				// The stored transcript never had this turn: recording happens only after
				// parsing succeeds.
				if n := len(history); n > 0 && history[n-1].Role == provider.RoleAssistant {
					if p.Logger != nil {
						p.Logger.Debugf("[Format Retry] omitting incomplete assistant turn (%d bytes)\n", len(history[n-1].Content))
					}
					history[n-1].Content = "[previous response was incomplete and omitted]"
				}
				p.Observer.Notice(incompleteNotice(meta, attempt+1, content))
			} else {
				p.Observer.Notice(fmt.Sprintf("Response format error, retrying (%d/%d): %v", attempt+1, maxFormatRetries, err))
			}

			correctionMsg := fmt.Sprintf(`[system] Your previous response was not valid JSON: %v
Reply with exactly ONE complete JSON object — every { needs a matching }, no prose and no code fences.
A tool action looks like:
%s
The other actions take a string payload: {"action":"done|ask|terminate","payload":"<string>","reason":"<short>"}`,
				err, ToolExample())
			if truncated {
				correctionMsg = fmt.Sprintf(`[system] Your previous response was incomplete (%s). Do not repeat the truncated text.
Reply with exactly ONE complete JSON object — every { needs a matching }, no prose and no code fences.
Keep this response short. If a file or report is too large, split it into several smaller write/edit calls.
A tool action looks like:
%s`,
					finishReasonExplanation(meta.FinishReason), ToolExample())
			}
			history = append(history, provider.Message{Role: provider.RoleUser, Content: correctionMsg})

			retryStart := time.Now()
			result, err := Chat(ctx, cfg, rp, p, history)
			content, history, meta.FinishReason, meta.Usage = result.Content, result.History, result.FinishReason, result.Usage
			if p.Logger != nil {
				p.Logger.Debugf("[Format Retry %d] request took %s\n", attempt+1, time.Since(retryStart))
			}
			if err != nil {
				return nil, history, err
			}
			if p.Logger != nil {
				p.Logger.Debugf("[AI Retry Output]:\n%s\n", content)
			}
		}
	}
	return nil, history, fmt.Errorf("AI failed to produce valid JSON after %d attempts: %w", maxFormatRetries, err)
}
