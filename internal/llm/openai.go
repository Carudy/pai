package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ---------------------------------------------------------------------------
// Built-in provider specs
// ---------------------------------------------------------------------------

var (
	openaiSpec = providerSpec{
		name:         "openai",
		baseURL:      "https://api.openai.com",
		apiPath:      "/v1/chat/completions",
		hasReasoning: true,
	}

	deepSeekSpec = providerSpec{
		name:         "deepseek",
		baseURL:      "https://api.deepseek.com",
		apiPath:      "/chat/completions",
		hasReasoning: true,
		bodyEnricher: func(body map[string]any, reasoning bool) {
			if reasoning {
				body["thinking"] = map[string]string{"type": "enabled"}
				body["reasoning_effort"] = "high"
			} else {
				body["thinking"] = map[string]string{"type": "disabled"}
			}
		},
	}

	mistralSpec = providerSpec{
		name:         "mistral",
		baseURL:      "https://api.mistral.ai",
		apiPath:      "/v1/chat/completions",
		hasReasoning: false,
	}
)

// ---------------------------------------------------------------------------
// Generic OpenAI-compatible provider
// ---------------------------------------------------------------------------

// openAIProvider implements the Provider interface for any OpenAI-compatible
// API (OpenAI, DeepSeek, Mistral, etc.).
type openAIProvider struct {
	apiKey string
	model  string
	spec   providerSpec
}

// newOpenAIProvider creates a new provider from the given spec.
func newOpenAIProvider(apiKey, model string, spec providerSpec) *openAIProvider {
	return &openAIProvider{apiKey: apiKey, model: model, spec: spec}
}

func (p *openAIProvider) Completion(ctx context.Context, params CompletionParams) (*ChatCompletion, error) {
	if params.Model == "" {
		params.Model = p.model
	}

	bodyMap := buildRequestBody(params, false, p.spec)
	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal request: %w", p.spec.name, err)
	}

	rawBody, err := p.doRequest(ctx, bodyBytes)
	if err != nil {
		return nil, err
	}

	return parseCompletion(rawBody)
}

func (p *openAIProvider) CompletionStream(ctx context.Context, params CompletionParams) (<-chan ChatCompletionChunk, <-chan error) {
	chunkChan := make(chan ChatCompletionChunk)
	errChan := make(chan error, 1)

	if params.Model == "" {
		params.Model = p.model
	}

	bodyMap := buildRequestBody(params, true, p.spec)
	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		errChan <- fmt.Errorf("%s: marshal request: %w", p.spec.name, err)
		close(chunkChan)
		return chunkChan, errChan
	}

	resp, err := p.doRequestRaw(ctx, bodyBytes)
	if err != nil {
		errChan <- err
		close(chunkChan)
		return chunkChan, errChan
	}

	go streamReader(ctx, resp, p.spec, chunkChan, errChan)

	return chunkChan, errChan
}

// ---------------------------------------------------------------------------
// Streaming goroutine
// ---------------------------------------------------------------------------

func streamReader(ctx context.Context, resp *http.Response, spec providerSpec, chunkChan chan<- ChatCompletionChunk, errChan chan<- error) {
	defer resp.Body.Close()
	defer close(chunkChan)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			errChan <- nil
			return
		}

		chunk := parseChunk(data)
		chunkChan <- chunk
	}

	if err := scanner.Err(); err != nil {
		errChan <- fmt.Errorf("%s: scan stream: %w", spec.name, err)
	} else {
		errChan <- nil
	}
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

// apiError reads the error body from a non-200 response, closes it, and
// returns a formatted error. Used by both doRequest and doRequestRaw.
func (p *openAIProvider) apiError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return fmt.Errorf("%s: API error (status %d): %s", p.spec.name, resp.StatusCode, string(body))
}

func (p *openAIProvider) doRequest(ctx context.Context, bodyBytes []byte) ([]byte, error) {
	req, err := p.newRequest(ctx, bodyBytes)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: do request: %w", p.spec.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, p.apiError(resp)
	}

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: read response: %w", p.spec.name, err)
	}
	return rawBody, nil
}

func (p *openAIProvider) doRequestRaw(ctx context.Context, bodyBytes []byte) (*http.Response, error) {
	req, err := p.newRequest(ctx, bodyBytes)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: do request: %w", p.spec.name, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, p.apiError(resp)
	}
	return resp, nil
}

func (p *openAIProvider) newRequest(ctx context.Context, bodyBytes []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.spec.baseURL+p.spec.apiPath, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("%s: create request: %w", p.spec.name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	return req, nil
}

// ---------------------------------------------------------------------------
// Request body construction
// ---------------------------------------------------------------------------

func buildRequestBody(params CompletionParams, stream bool, spec providerSpec) map[string]any {
	body := map[string]any{
		"model":    params.Model,
		"messages": params.Messages,
		"stream":   stream,
	}

	if params.ResponseFormat != nil {
		body["response_format"] = params.ResponseFormat
	}

	if spec.hasReasoning {
		if spec.bodyEnricher != nil {
			spec.bodyEnricher(body, params.ReasoningEffort != ReasoningEffortNone)
		} else {
			// Default: standard OpenAI reasoning_effort.
			if params.ReasoningEffort != "" && params.ReasoningEffort != ReasoningEffortNone {
				body["reasoning_effort"] = string(params.ReasoningEffort)
			}
		}
	}

	return body
}

// ---------------------------------------------------------------------------
// Response parsing (standard OpenAI-compatible JSON → typed structs)
// ---------------------------------------------------------------------------

// rawCompletion mirrors the OpenAI-compatible chat completion JSON shape.
type rawCompletion struct {
	ID      string      `json:"id"`
	Choices []rawChoice `json:"choices"`
	Usage   *rawUsage   `json:"usage,omitempty"`
}

type rawChoice struct {
	Index            int        `json:"index"`
	FinishReason     string     `json:"finish_reason"`
	Message          rawMessage `json:"message"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
}

type rawMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type rawUsage struct {
	PromptTokens            int              `json:"prompt_tokens"`
	CompletionTokens        int              `json:"completion_tokens"`
	TotalTokens             int              `json:"total_tokens"`
	CompletionTokensDetails *rawUsageDetails `json:"completion_tokens_details,omitempty"`
}

type rawUsageDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

func parseCompletion(raw []byte) (*ChatCompletion, error) {
	var r rawCompletion
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("parse completion response: %w", err)
	}

	cc := &ChatCompletion{
		ID: r.ID,
	}

	if r.Usage != nil {
		u := &Usage{
			PromptTokens:     r.Usage.PromptTokens,
			CompletionTokens: r.Usage.CompletionTokens,
			TotalTokens:      r.Usage.TotalTokens,
		}
		if d := r.Usage.CompletionTokensDetails; d != nil {
			u.ReasoningTokens = d.ReasoningTokens
		}
		cc.Usage = u
	}

	for _, rc := range r.Choices {
		c := Choice{
			Index:        rc.Index,
			FinishReason: rc.FinishReason,
			Message: Message{
				Role:    rc.Message.Role,
				Content: rc.Message.Content,
			},
		}
		if rc.ReasoningContent != "" {
			c.Reasoning = &Reasoning{Content: rc.ReasoningContent}
		}
		cc.Choices = append(cc.Choices, c)
	}

	return cc, nil
}

// rawChunk mirrors the OpenAI-compatible streaming chunk JSON shape.
type rawChunk struct {
	ID      string           `json:"id"`
	Choices []rawChunkChoice `json:"choices"`
}

type rawChunkChoice struct {
	Index        int           `json:"index"`
	FinishReason string        `json:"finish_reason"`
	Delta        rawChunkDelta `json:"delta"`
}

type rawChunkDelta struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

func parseChunk(data string) ChatCompletionChunk {
	var r rawChunk
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		return ChatCompletionChunk{}
	}

	cc := ChatCompletionChunk{
		ID: r.ID,
	}

	for _, rc := range r.Choices {
		chunk := ChunkChoice{
			Index:        rc.Index,
			FinishReason: rc.FinishReason,
			Delta: ChunkDelta{
				Role:    rc.Delta.Role,
				Content: rc.Delta.Content,
			},
		}
		if rc.Delta.ReasoningContent != "" {
			chunk.Delta.Reasoning = &Reasoning{Content: rc.Delta.ReasoningContent}
		}
		cc.Choices = append(cc.Choices, chunk)
	}

	return cc
}
