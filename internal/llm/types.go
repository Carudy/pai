package llm

import (
	"context"
	"fmt"
)

// Message roles
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// ReasoningEffort levels for extended thinking.
type ReasoningEffort string

const (
	ReasoningEffortLow    ReasoningEffort = "low"
	ReasoningEffortMedium ReasoningEffort = "medium"
	ReasoningEffortHigh   ReasoningEffort = "high"
	ReasoningEffortNone   ReasoningEffort = "none"
)

// Message in a chat conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ResponseFormat specifies the expected response format.
type ResponseFormat struct {
	Type string `json:"type"`
}

// CompletionParams for chat completion requests.
type CompletionParams struct {
	Model           string          `json:"model"`
	Messages        []Message       `json:"messages"`
	Stream          bool            `json:"stream,omitempty"`
	ReasoningEffort ReasoningEffort `json:"reasoning_effort,omitempty"`
	ResponseFormat  *ResponseFormat `json:"response_format,omitempty"`
}

// ChatCompletion for blocking responses.
type ChatCompletion struct {
	ID      string   `json:"id"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Choice in a blocking response.
type Choice struct {
	Index        int        `json:"index"`
	Message      Message    `json:"message"`
	FinishReason string     `json:"finish_reason,omitempty"`
	Reasoning    *Reasoning `json:"reasoning,omitempty"`
}

// ChatCompletionChunk for streaming responses.
type ChatCompletionChunk struct {
	ID      string        `json:"id"`
	Choices []ChunkChoice `json:"choices"`
	Usage   *Usage        `json:"usage,omitempty"`
}

// ChunkChoice in a streaming chunk.
type ChunkChoice struct {
	Index        int        `json:"index"`
	Delta        ChunkDelta `json:"delta"`
	FinishReason string     `json:"finish_reason,omitempty"`
}

// ChunkDelta in a streaming chunk.
type ChunkDelta struct {
	Role      string     `json:"role,omitempty"`
	Content   string     `json:"content,omitempty"`
	Reasoning *Reasoning `json:"reasoning,omitempty"`
}

// Reasoning content from the model.
type Reasoning struct {
	Content string `json:"content"`
}

// Usage token counts.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
}

// Total returns the total token count: uses the API-reported TotalTokens if
// non-zero; otherwise falls back to PromptTokens + CompletionTokens.
func (u *Usage) Total() int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.PromptTokens + u.CompletionTokens
}

// FormatTokens returns a human-readable token count (e.g. "42", "1.2K", "12K", "1.5M").
func FormatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		v := float64(n) / 1_000_000
		if v >= 10 {
			return fmt.Sprintf("%.0fM", v)
		}
		return fmt.Sprintf("%.1fM", v)
	case n >= 10_000:
		return fmt.Sprintf("%.0fK", float64(n)/1000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// Provider is the interface that all LLM providers must implement.
type Provider interface {
	Completion(ctx context.Context, params CompletionParams) (*ChatCompletion, error)
	CompletionStream(ctx context.Context, params CompletionParams) (<-chan ChatCompletionChunk, <-chan error)
}
