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

	"github.com/tidwall/gjson"
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

	return parseCompletion(rawBody), nil
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
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: API error (status %d): %s", p.spec.name, resp.StatusCode, string(body))
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
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("%s: API error (status %d): %s", p.spec.name, resp.StatusCode, string(body))
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
				body["reasoning_effort"] = "high"
			}
		}
	}

	return body
}

// ---------------------------------------------------------------------------
// Response parsing (standard OpenAI-compatible JSON → typed structs)
// ---------------------------------------------------------------------------

func parseCompletion(raw []byte) *ChatCompletion {
	result := gjson.ParseBytes(raw)

	cc := &ChatCompletion{
		ID: result.Get("id").String(),
	}

	// Usage
	if usage := result.Get("usage"); usage.Exists() {
		cc.Usage = &Usage{
			PromptTokens:     int(usage.Get("prompt_tokens").Int()),
			CompletionTokens: int(usage.Get("completion_tokens").Int()),
			TotalTokens:      int(usage.Get("total_tokens").Int()),
		}
		if rt := usage.Get("completion_tokens_details.reasoning_tokens"); rt.Exists() {
			cc.Usage.ReasoningTokens = int(rt.Int())
		}
	}

	// Choices
	result.Get("choices").ForEach(func(_, choice gjson.Result) bool {
		c := Choice{
			Index:        int(choice.Get("index").Int()),
			FinishReason: choice.Get("finish_reason").String(),
			Message: Message{
				Role:    choice.Get("message.role").String(),
				Content: choice.Get("message.content").String(),
			},
		}

		if rc := choice.Get("reasoning_content"); rc.Exists() && rc.String() != "" {
			c.Reasoning = &Reasoning{Content: rc.String()}
		}

		cc.Choices = append(cc.Choices, c)
		return true
	})

	return cc
}

func parseChunk(data string) ChatCompletionChunk {
	result := gjson.Parse(data)

	cc := ChatCompletionChunk{
		ID: result.Get("id").String(),
	}

	result.Get("choices").ForEach(func(_, choice gjson.Result) bool {
		chunk := ChunkChoice{
			Index:        int(choice.Get("index").Int()),
			FinishReason: choice.Get("finish_reason").String(),
			Delta: ChunkDelta{
				Role:    choice.Get("delta.role").String(),
				Content: choice.Get("delta.content").String(),
			},
		}

		if rc := choice.Get("delta.reasoning_content"); rc.Exists() && rc.String() != "" {
			chunk.Delta.Reasoning = &Reasoning{Content: rc.String()}
		}

		cc.Choices = append(cc.Choices, chunk)
		return true
	})

	return cc
}
