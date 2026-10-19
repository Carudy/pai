package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ---------------------------------------------------------------------------
// Built-in provider specs
// // bodyEnricher: func(body map[string]any, reasoning bool) {
// // 	if reasoning {
// // 		body["thinking"] = map[string]string{"type": "enabled"}
// // 		body["reasoning_effort"] = "low"
// // 	} else {
// // 		body["thinking"] = map[string]string{"type": "disabled"}
// // 	}
// //},
// ---------------------------------------------------------------------------

var builtInProviders = map[string]providerSpec{
	"openai": {
		name:         "openai",
		baseURL:      "https://api.openai.com/v1/chat/completions",
		responsesURL: "https://api.openai.com/v1/responses",
	},
	"deepseek": {
		name:    "deepseek",
		baseURL: "https://api.deepseek.com/chat/completions",
	},
	"mistral": {
		name:    "mistral",
		baseURL: "https://api.mistral.ai/v1/chat/completions",
	},
}

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

	rawBody, err := postJSON(ctx, p.spec.name, p.spec.baseURL, p.apiKey, p.spec.headers, bodyBytes)
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

	resp, err := postJSONStream(ctx, p.spec.name, p.spec.baseURL, p.apiKey, p.spec.headers, bodyBytes)
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
// Request body construction
// ---------------------------------------------------------------------------

func buildRequestBody(params CompletionParams, stream bool, spec providerSpec) map[string]any {
	body := map[string]any{
		"model":    params.Model,
		"messages": params.Messages,
		"stream":   stream,
	}

	if stream {
		// Ask for the usage chunk that ends the stream. OpenAI-compatible servers
		// omit it otherwise, which would leave PAI with no prompt size to compare
		// against summarize_after_tokens — silently disabling compaction.
		body["stream_options"] = map[string]any{"include_usage": true}
	}

	if params.ResponseFormat != nil {
		body["response_format"] = params.ResponseFormat
	}

	if spec.bodyEnricher != nil {
		spec.bodyEnricher(body, params.ReasoningEffort != ReasoningEffortNone)
	} else {
		if params.ReasoningEffort != "" && params.ReasoningEffort != ReasoningEffortNone {
			body["reasoning_effort"] = string(params.ReasoningEffort)
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
	PromptTokens        int              `json:"prompt_tokens"`
	CompletionTokens    int              `json:"completion_tokens"`
	TotalTokens         int              `json:"total_tokens"`
	PromptTokensDetails *rawUsageDetails `json:"prompt_tokens_details,omitempty"`

	CompletionTokensDetails *rawUsageDetails `json:"completion_tokens_details,omitempty"`

	// Cache accounting has no single spelling across OpenAI-compatible servers.
	// These are the shapes seen in the wild, most standard first; unknown fields are
	// simply absent, which cachedPromptTokens reads as "not reported".
	CachedTokens         *int `json:"cached_tokens,omitempty"`
	PromptCacheHitTokens *int `json:"prompt_cache_hit_tokens,omitempty"`
	CacheReadInputTokens *int `json:"cache_read_input_tokens,omitempty"`
}

type rawUsageDetails struct {
	ReasoningTokens int  `json:"reasoning_tokens,omitempty"`
	CachedTokens    *int `json:"cached_tokens,omitempty"`
}

// cachedPromptTokens returns how many prompt tokens the server served from its
// prefix cache, reading whichever spelling this server uses. It returns 0 when the
// response carries no cache accounting at all, which callers treat as "unknown"
// rather than as a miss — most OpenAI-compatible servers report nothing.
func cachedPromptTokens(u *rawUsage) int {
	if u == nil {
		return 0
	}
	if d := u.PromptTokensDetails; d != nil && d.CachedTokens != nil {
		return *d.CachedTokens
	}
	for _, c := range []*int{u.CachedTokens, u.PromptCacheHitTokens, u.CacheReadInputTokens} {
		if c != nil {
			return *c
		}
	}
	return 0
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
			PromptTokens:       r.Usage.PromptTokens,
			CompletionTokens:   r.Usage.CompletionTokens,
			TotalTokens:        r.Usage.TotalTokens,
			CachedPromptTokens: cachedPromptTokens(r.Usage),
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
	Usage   *rawUsage        `json:"usage,omitempty"`
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

	if r.Usage != nil {
		cc.Usage = &Usage{
			PromptTokens:       r.Usage.PromptTokens,
			CompletionTokens:   r.Usage.CompletionTokens,
			TotalTokens:        r.Usage.TotalTokens,
			CachedPromptTokens: cachedPromptTokens(r.Usage),
		}
		if d := r.Usage.CompletionTokensDetails; d != nil {
			cc.Usage.ReasoningTokens = d.ReasoningTokens
		}
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
