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

// openaiProvider implements Provider for the OpenAI API.
type openaiProvider struct {
	apiKey    string
	model     string
	baseURL   string
	reasoning bool
}

func (p *openaiProvider) Completion(ctx context.Context, params CompletionParams) (*ChatCompletion, error) {
	if params.Model == "" {
		params.Model = p.model
	}

	bodyMap := map[string]any{
		"model":    params.Model,
		"messages": params.Messages,
		"stream":   false,
	}

	if p.reasoning {
		bodyMap["reasoning_effort"] = "high"
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai: API error (status %d): %s", resp.StatusCode, string(body))
	}

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: read response: %w", err)
	}

	return parseOpenAICompletion(rawBody), nil
}

func (p *openaiProvider) CompletionStream(ctx context.Context, params CompletionParams) (<-chan ChatCompletionChunk, <-chan error) {
	chunkChan := make(chan ChatCompletionChunk)
	errChan := make(chan error, 1)

	if params.Model == "" {
		params.Model = p.model
	}

	bodyMap := map[string]any{
		"model":    params.Model,
		"messages": params.Messages,
		"stream":   true,
	}

	if p.reasoning {
		bodyMap["reasoning_effort"] = "high"
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		errChan <- fmt.Errorf("openai: marshal request: %w", err)
		close(chunkChan)
		return chunkChan, errChan
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		errChan <- fmt.Errorf("openai: create request: %w", err)
		close(chunkChan)
		return chunkChan, errChan
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		errChan <- fmt.Errorf("openai: do request: %w", err)
		close(chunkChan)
		return chunkChan, errChan
	}

	go func() {
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

			chunk := parseOpenAIChunk(data)
			chunkChan <- chunk
		}

		if err := scanner.Err(); err != nil {
			errChan <- fmt.Errorf("openai: scan stream: %w", err)
		} else {
			errChan <- nil
		}
	}()

	return chunkChan, errChan
}

// parseOpenAICompletion parses a standard OpenAI-compatible blocking response.
func parseOpenAICompletion(raw []byte) *ChatCompletion {
	result := gjson.ParseBytes(raw)

	cc := &ChatCompletion{
		ID: result.Get("id").String(),
	}

	if usage := result.Get("usage"); usage.Exists() {
		cc.Usage = &Usage{
			PromptTokens:     int(usage.Get("prompt_tokens").Int()),
			CompletionTokens: int(usage.Get("completion_tokens").Int()),
			TotalTokens:      int(usage.Get("total_tokens").Int()),
			ReasoningTokens:  int(usage.Get("completion_tokens_details.reasoning_tokens").Int()),
		}
	}

	choices := result.Get("choices")
	choices.ForEach(func(_, choice gjson.Result) bool {
		c := Choice{
			Index:        int(choice.Get("index").Int()),
			FinishReason: choice.Get("finish_reason").String(),
			Message: Message{
				Role:    choice.Get("message.role").String(),
				Content: choice.Get("message.content").String(),
			},
		}

		// Extract reasoning_content from the message (non-standard field).
		if rc := choice.Get("message.reasoning_content"); rc.Exists() && rc.String() != "" {
			c.Reasoning = &Reasoning{Content: rc.String()}
		}

		cc.Choices = append(cc.Choices, c)
		return true
	})

	return cc
}

// parseOpenAIChunk parses a standard OpenAI-compatible streaming chunk.
func parseOpenAIChunk(data string) ChatCompletionChunk {
	result := gjson.Parse(data)

	cc := ChatCompletionChunk{
		ID: result.Get("id").String(),
	}

	choices := result.Get("choices")
	choices.ForEach(func(_, choice gjson.Result) bool {
		chunk := ChunkChoice{
			Index:        int(choice.Get("index").Int()),
			FinishReason: choice.Get("finish_reason").String(),
			Delta: ChunkDelta{
				Role:    choice.Get("delta.role").String(),
				Content: choice.Get("delta.content").String(),
			},
		}

		// Extract reasoning_content from delta (non-standard field).
		if rc := choice.Get("delta.reasoning_content"); rc.Exists() && rc.String() != "" {
			chunk.Delta.Reasoning = &Reasoning{Content: rc.String()}
		}

		cc.Choices = append(cc.Choices, chunk)
		return true
	})

	return cc
}
