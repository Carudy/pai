package llm

import "context"

// Message roles
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Finish reasons
const (
	FinishReasonStop   = "stop"
	FinishReasonLength = "length"
)

// ReasoningEffort levels for extended thinking.
type ReasoningEffort string

const (
	ReasoningEffortAuto   ReasoningEffort = "auto"
	ReasoningEffortHigh   ReasoningEffort = "high"
	ReasoningEffortLow    ReasoningEffort = "low"
	ReasoningEffortMedium ReasoningEffort = "medium"
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

// Provider is the interface that all LLM providers must implement.
type Provider interface {
	Completion(ctx context.Context, params CompletionParams) (*ChatCompletion, error)
	CompletionStream(ctx context.Context, params CompletionParams) (<-chan ChatCompletionChunk, <-chan error)
}
