package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// responsesProvider talks to OpenAI's Responses API (POST /responses), the wire
// protocol Codex-style clients use.
//
// Two differences from chat completions are worth stating, because they are
// flattened away here so the rest of PAI never sees them:
//
//   - The stream is a sequence of *named* SSE events (response.output_text.delta,
//     response.reasoning_summary_text.delta, response.completed, …) rather than
//     one delta shape repeated.
//   - Reasoning is surfaced as a model-written *summary* (and only when the
//     request asks for one), not as raw chain of thought.
//
// Requests stay stateless: the whole conversation is replayed as input items on
// every call (store:false), so nothing depends on server-side response state.
type responsesProvider struct {
	apiKey string
	model  string
	spec   providerSpec
}

func newResponsesProvider(apiKey, model string, spec providerSpec) *responsesProvider {
	return &responsesProvider{apiKey: apiKey, model: model, spec: spec}
}

func (p *responsesProvider) Completion(ctx context.Context, params CompletionParams) (*ChatCompletion, error) {
	if params.Model == "" {
		params.Model = p.model
	}

	body, err := json.Marshal(responsesBody(params, false))
	if err != nil {
		return nil, fmt.Errorf("%s: marshal request: %w", p.spec.name, err)
	}

	raw, err := postJSON(ctx, p.spec.name, p.spec.baseURL, p.apiKey, p.spec.headers, body)
	if err != nil {
		return nil, err
	}
	return parseResponses(raw)
}

func (p *responsesProvider) CompletionStream(ctx context.Context, params CompletionParams) (<-chan ChatCompletionChunk, <-chan error) {
	chunkChan := make(chan ChatCompletionChunk)
	errChan := make(chan error, 1)

	if params.Model == "" {
		params.Model = p.model
	}

	body, err := json.Marshal(responsesBody(params, true))
	if err != nil {
		errChan <- fmt.Errorf("%s: marshal request: %w", p.spec.name, err)
		close(chunkChan)
		return chunkChan, errChan
	}

	resp, err := postJSONStream(ctx, p.spec.name, p.spec.baseURL, p.apiKey, p.spec.headers, body)
	if err != nil {
		errChan <- err
		close(chunkChan)
		return chunkChan, errChan
	}

	go responsesStreamReader(ctx, resp, p.spec, chunkChan, errChan)
	return chunkChan, errChan
}

// ---------------------------------------------------------------------------
// Request
// ---------------------------------------------------------------------------

// responsesBody builds the Responses request. A system message becomes
// `instructions` and the conversation becomes `input` items, so no server-side
// state is needed between turns (store:false).
func responsesBody(params CompletionParams, stream bool) map[string]any {
	instructions, input := responsesInput(params.Messages)

	body := map[string]any{
		"model":  params.Model,
		"stream": stream,
		"store":  false,
		// input is required; a lone instruction with no turns still needs a value.
		"input": input,
	}
	if instructions != "" {
		body["instructions"] = instructions
	}
	if effort := string(params.ReasoningEffort); effort != "" && effort != string(ReasoningEffortNone) {
		// A summary must be asked for explicitly; without it the API returns no
		// reasoning text even on a reasoning model.
		body["reasoning"] = map[string]any{"effort": effort, "summary": "auto"}
	}
	if params.ResponseFormat != nil {
		body["text"] = map[string]any{"format": map[string]any{"type": params.ResponseFormat.Type}}
	}
	return body
}

// responsesInput splits messages into the `instructions` string and the `input`
// array. User and assistant turns replay as typed content parts (input_text /
// output_text), which is the shape the API documents and the official SDK sends.
func responsesInput(msgs []Message) (instructions string, input []map[string]any) {
	input = make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleSystem:
			if instructions != "" {
				instructions += "\n\n"
			}
			instructions += m.Content
		case RoleAssistant:
			input = append(input, map[string]any{
				"role":    RoleAssistant,
				"content": []map[string]any{{"type": "output_text", "text": m.Content}},
			})
		default:
			input = append(input, map[string]any{
				"role":    RoleUser,
				"content": []map[string]any{{"type": "input_text", "text": m.Content}},
			})
		}
	}
	return instructions, input
}

// ---------------------------------------------------------------------------
// Response parsing
// ---------------------------------------------------------------------------

// rawResponses mirrors the Responses API object, both the terminal event's
// `response` payload and a non-streaming reply.
type rawResponses struct {
	ID                string             `json:"id"`
	Status            string             `json:"status"`
	Error             *rawResponsesErr   `json:"error"`
	IncompleteDetails *incompleteReason  `json:"incomplete_details"`
	Output            []rawResponsesItem `json:"output"`
	Usage             *rawResponsesUsage `json:"usage"`
}

type rawResponsesErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type incompleteReason struct {
	Reason string `json:"reason"`
}

type rawResponsesItem struct {
	Type    string `json:"type"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	// A reasoning item carries its text as a summary, not as content.
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
}

type rawResponsesUsage struct {
	InputTokens         int                  `json:"input_tokens"`
	OutputTokens        int                  `json:"output_tokens"`
	TotalTokens         int                  `json:"total_tokens"`
	OutputTokensDetails *reasoningTokenCount `json:"output_tokens_details"`
}

type reasoningTokenCount struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// responsesParts folds the output items into the assistant's text and its
// reasoning summary. Both are concatenations because either may arrive split
// across several items.
func responsesParts(r *rawResponses) (text, reasoning string) {
	var out, think strings.Builder
	for _, item := range r.Output {
		for _, part := range item.Content {
			if part.Type == "output_text" {
				out.WriteString(part.Text)
			}
		}
		for _, part := range item.Summary {
			think.WriteString(part.Text)
		}
	}
	return out.String(), think.String()
}

// responsesToUsage maps the Responses token accounting onto PAI's Usage.
func responsesToUsage(u *rawResponsesUsage) *Usage {
	if u == nil {
		return nil
	}
	usage := &Usage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
	}
	if u.OutputTokensDetails != nil {
		usage.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
	}
	return usage
}

func parseResponses(raw []byte) (*ChatCompletion, error) {
	var r rawResponses
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("parse responses response: %w", err)
	}
	if err := responsesStatusError(&r); err != nil {
		return nil, err
	}

	text, reasoning := responsesParts(&r)
	choice := Choice{Index: 0, Message: Message{Role: RoleAssistant, Content: text}}
	if reasoning != "" {
		choice.Reasoning = &Reasoning{Content: reasoning}
	}
	return &ChatCompletion{ID: r.ID, Choices: []Choice{choice}, Usage: responsesToUsage(r.Usage)}, nil
}

// responsesStatusError turns a failed or truncated response into an error rather
// than a half-parsed success; the caller feeds the error back to the model.
func responsesStatusError(r *rawResponses) error {
	if r.Error != nil && r.Error.Message != "" {
		return fmt.Errorf("responses API (%s): %s", r.Error.Code, r.Error.Message)
	}
	switch r.Status {
	case "failed":
		return errors.New("responses API: request failed")
	case "incomplete":
		reason := "unknown"
		if r.IncompleteDetails != nil && r.IncompleteDetails.Reason != "" {
			reason = r.IncompleteDetails.Reason
		}
		return fmt.Errorf("responses API: response incomplete (%s)", reason)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Streaming
// ---------------------------------------------------------------------------

// rawResponsesEvent is the envelope shared by the named stream events. Only the
// fields needed to render text, reasoning, usage, and failures are decoded.
type rawResponsesEvent struct {
	Type     string           `json:"type"`
	Delta    string           `json:"delta"`
	Code     string           `json:"code"`
	Message  string           `json:"message"`
	Error    *rawResponsesErr `json:"error"`
	Response *rawResponses    `json:"response"`
}

// responsesStreamReader decodes the named SSE events and republishes the ones
// that matter as chat-completions-style deltas, so the consumer stays unchanged.
func responsesStreamReader(_ context.Context, resp *http.Response, spec providerSpec, chunkChan chan<- ChatCompletionChunk, errChan chan<- error) {
	defer resp.Body.Close()
	defer close(chunkChan)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		// `event:` lines and the blank separators carry no payload we need; the
		// JSON on the `data:` line repeats the event type.
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		// The API ends on a terminal event, but chat-completions-style gateways
		// append [DONE]; accept both.
		if data == "[DONE]" {
			errChan <- nil
			return
		}

		var ev rawResponsesEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			errChan <- fmt.Errorf("%s: parse stream event: %w", spec.name, err)
			return
		}

		switch ev.Type {
		case "response.output_text.delta":
			chunkChan <- ChatCompletionChunk{Choices: []ChunkChoice{{Delta: ChunkDelta{Content: ev.Delta}}}}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			chunkChan <- ChatCompletionChunk{Choices: []ChunkChoice{{Delta: ChunkDelta{Reasoning: &Reasoning{Content: ev.Delta}}}}}
		case "response.completed":
			// Usage only arrives on the terminal event.
			if ev.Response != nil && ev.Response.Usage != nil {
				chunkChan <- ChatCompletionChunk{Usage: responsesToUsage(ev.Response.Usage)}
			}
			errChan <- nil
			return
		case "response.failed", "response.incomplete", "error":
			errChan <- responsesEventError(ev)
			return
		}
	}

	if err := scanner.Err(); err != nil {
		errChan <- fmt.Errorf("%s: scan stream: %w", spec.name, err)
		return
	}
	// Some gateways just close the socket without a terminal event.
	errChan <- nil
}

// responsesEventError extracts the best error text a failure event carries: a
// nested response.error for lifecycle events, or a top-level code/message for
// the bare `error` event.
func responsesEventError(ev rawResponsesEvent) error {
	switch {
	case ev.Error != nil && ev.Error.Message != "":
		return fmt.Errorf("responses API (%s): %s", ev.Error.Code, ev.Error.Message)
	case ev.Response != nil && ev.Response.Error != nil && ev.Response.Error.Message != "":
		return fmt.Errorf("responses API (%s): %s", ev.Response.Error.Code, ev.Response.Error.Message)
	case ev.Code != "" || ev.Message != "":
		return fmt.Errorf("responses API (%s): %s", ev.Code, ev.Message)
	case ev.Type == "response.incomplete":
		if ev.Response != nil && ev.Response.IncompleteDetails != nil && ev.Response.IncompleteDetails.Reason != "" {
			return fmt.Errorf("responses API: response incomplete (%s)", ev.Response.IncompleteDetails.Reason)
		}
		return errors.New("responses API: response incomplete")
	}
	return errors.New("responses API: request failed")
}
