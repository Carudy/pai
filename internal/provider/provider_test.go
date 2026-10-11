package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Protocol selection
// ---------------------------------------------------------------------------

func TestCreateClientProtocols(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ProviderConfig
		wantErr string
	}{
		{"responses for a provider without one", ProviderConfig{Name: "deepseek", Protocol: ProtocolResponses}, "set base_url"},
		{"unknown protocol", ProviderConfig{Name: "openai", Protocol: "grpc"}, "unknown protocol"},
		{"unknown provider without base_url", ProviderConfig{Name: "who"}, "unsupported provider"},
		{"known chat provider", ProviderConfig{Name: "openai"}, ""},
		{"responses provider", ProviderConfig{Name: "openai", Protocol: ProtocolResponses}, ""},
		{"unknown provider with base_url", ProviderConfig{Name: "who", BaseURL: "http://x/v1/chat/completions"}, ""},
		{"base_url overrides the built-in", ProviderConfig{Name: "openai", BaseURL: "http://x/v1/chat/completions"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CreateClient(tc.cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Chat completions (locks the shared HTTP plumbing)
// ---------------------------------------------------------------------------

func TestChatCompletion(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		if auth := r.Header.Get("Authorization"); auth != "Bearer key" {
			t.Errorf("authorization = %q", auth)
		}
		io.WriteString(w, `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer srv.Close()

	p, err := CreateClient(ProviderConfig{Name: "openai", APIKey: "key", Model: "m", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	cc, err := p.Completion(context.Background(), CompletionParams{Messages: []Message{{Role: RoleUser, Content: "yo"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cc.Choices) != 1 || cc.Choices[0].Message.Content != "hi" {
		t.Fatalf("choices = %+v", cc.Choices)
	}
	if cc.Usage == nil || cc.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v", cc.Usage)
	}
	if gotBody["model"] != "m" {
		t.Errorf("model = %v", gotBody["model"])
	}
	if gotBody["stream"] != false {
		t.Errorf("stream = %v, want false", gotBody["stream"])
	}
}

func TestChatCompletionStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"think\"}}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hel\"}}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"}}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := CreateClient(ProviderConfig{Name: "openai", Model: "m", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	chunks, errs := p.CompletionStream(context.Background(), CompletionParams{Stream: true})
	text, reasoning := collectStream(t, chunks, errs)
	if text != "hello" {
		t.Errorf("text = %q", text)
	}
	if reasoning != "think" {
		t.Errorf("reasoning = %q", reasoning)
	}
}

// ---------------------------------------------------------------------------
// Responses API
// ---------------------------------------------------------------------------

func TestResponsesCompletion(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		io.WriteString(w, `{"id":"r1","status":"completed","output":[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"because"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}
		],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"output_tokens_details":{"reasoning_tokens":4}}}`)
	}))
	defer srv.Close()

	p, err := CreateClient(ProviderConfig{Name: "openai", APIKey: "key", Model: "m", Protocol: ProtocolResponses, BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	cc, err := p.Completion(context.Background(), CompletionParams{
		Messages: []Message{
			{Role: RoleSystem, Content: "sys"},
			{Role: RoleUser, Content: "yo"},
			{Role: RoleAssistant, Content: "prior"},
			{Role: RoleUser, Content: "again"},
		},
		ReasoningEffort: ReasoningEffortHigh,
		ResponseFormat:  &ResponseFormat{Type: "json_object"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(cc.Choices) != 1 || cc.Choices[0].Message.Content != "hello" {
		t.Fatalf("choices = %+v", cc.Choices)
	}
	if cc.Choices[0].Reasoning == nil || cc.Choices[0].Reasoning.Content != "because" {
		t.Fatalf("reasoning = %+v", cc.Choices[0].Reasoning)
	}
	if cc.Usage == nil || cc.Usage.PromptTokens != 10 || cc.Usage.CompletionTokens != 5 || cc.Usage.TotalTokens != 15 || cc.Usage.ReasoningTokens != 4 {
		t.Fatalf("usage = %+v", cc.Usage)
	}

	// Request shape: stateless, system message in instructions, typed input parts.
	if gotBody["store"] != false {
		t.Errorf("store = %v, want false", gotBody["store"])
	}
	if gotBody["instructions"] != "sys" {
		t.Errorf("instructions = %v", gotBody["instructions"])
	}
	if gotBody["reasoning"].(map[string]any)["effort"] != "high" {
		t.Errorf("reasoning = %v", gotBody["reasoning"])
	}
	if gotBody["reasoning"].(map[string]any)["summary"] != "auto" {
		t.Errorf("reasoning summary = %v", gotBody["reasoning"])
	}
	if gotBody["text"].(map[string]any)["format"].(map[string]any)["type"] != "json_object" {
		t.Errorf("text = %v", gotBody["text"])
	}
	input := gotBody["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input has %d items, want 3 (system became instructions)", len(input))
	}
	first := input[0].(map[string]any)
	if first["role"] != RoleUser || first["content"].([]any)[0].(map[string]any)["type"] != "input_text" {
		t.Errorf("first input item = %v", first)
	}
	second := input[1].(map[string]any)
	if second["role"] != RoleAssistant || second["content"].([]any)[0].(map[string]any)["type"] != "output_text" {
		t.Errorf("assistant input item = %v", second)
	}
}

// Reasoning must be requested only when it is turned on; asking for a summary on
// a plain call would waste tokens.
func TestResponsesOmitsReasoningWhenOff(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		io.WriteString(w, `{"id":"r1","status":"completed","output":[]}`)
	}))
	defer srv.Close()

	p, _ := CreateClient(ProviderConfig{Name: "openai", Model: "m", Protocol: ProtocolResponses, BaseURL: srv.URL})
	if _, err := p.Completion(context.Background(), CompletionParams{
		Messages:        []Message{{Role: RoleUser, Content: "yo"}},
		ReasoningEffort: ReasoningEffortNone,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotBody["reasoning"]; ok {
		t.Errorf("reasoning sent while off: %v", gotBody["reasoning"])
	}
}

func TestResponsesStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"status\":\"in_progress\"}}\n\n")
		io.WriteString(w, "event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"think\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"he\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"llo\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3,\"total_tokens\":10}}}\n\n")
	}))
	defer srv.Close()

	p, _ := CreateClient(ProviderConfig{Name: "openai", Model: "m", Protocol: ProtocolResponses, BaseURL: srv.URL})
	chunks, errs := p.CompletionStream(context.Background(), CompletionParams{Stream: true})

	text, reasoning := collectStream(t, chunks, errs)
	if text != "hello" {
		t.Errorf("text = %q", text)
	}
	if reasoning != "think" {
		t.Errorf("reasoning = %q", reasoning)
	}
}

// The usage chunk arrives last, after the text deltas; keep it.
func TestResponsesStreamUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3,\"total_tokens\":10}}}\n\n")
	}))
	defer srv.Close()

	p, _ := CreateClient(ProviderConfig{Name: "openai", Model: "m", Protocol: ProtocolResponses, BaseURL: srv.URL})
	chunks, errs := p.CompletionStream(context.Background(), CompletionParams{Stream: true})

	var usage *Usage
	for chunk := range chunks {
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	if err := <-errs; err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if usage == nil || usage.TotalTokens != 10 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestResponsesStreamTerminators(t *testing.T) {
	cases := []struct {
		name      string
		frames    string
		wantErr   string
		wantClean bool
	}{
		{
			name:      "chat-style [DONE] is tolerated",
			frames:    "data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\ndata: [DONE]\n\n",
			wantClean: true,
		},
		{
			name:    "response.failed",
			frames:  "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"boom\"}}}\n\n",
			wantErr: "boom",
		},
		{
			name:    "bare error event",
			frames:  "data: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\",\"message\":\"slow down\"}\n\n",
			wantErr: "slow down",
		},
		{
			name:    "incomplete",
			frames:  "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n",
			wantErr: "max_output_tokens",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, tc.frames)
			}))
			defer srv.Close()

			p, _ := CreateClient(ProviderConfig{Name: "openai", Model: "m", Protocol: ProtocolResponses, BaseURL: srv.URL})
			chunks, errs := p.CompletionStream(context.Background(), CompletionParams{Stream: true})
			for range chunks {
			}
			err := <-errs
			if tc.wantClean {
				if err != nil {
					t.Fatalf("error = %v, want clean end", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// A non-2xx status must surface the provider's error body, not an empty reply.
func TestResponsesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()

	p, _ := CreateClient(ProviderConfig{Name: "openai", Model: "m", Protocol: ProtocolResponses, BaseURL: srv.URL})
	_, err := p.Completion(context.Background(), CompletionParams{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("error = %v, want the response body", err)
	}
}

// ---------------------------------------------------------------------------
// Request headers
// ---------------------------------------------------------------------------

// PAI always identifies itself, and the OpenCode Go gateway additionally needs a
// routing session id; both must be present without any user configuration.
func TestResolveHeadersDefaults(t *testing.T) {
	openCode := resolveHeaders(nil, "https://opencode.ai/zen/go/v1/chat/completions")
	if got := openCode["User-Agent"]; got != defaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", got, defaultUserAgent)
	}
	if openCode[openCodeSessionHeader] == "" {
		t.Error("opencode host got no session id")
	}

	other := resolveHeaders(nil, "https://api.example.com/v1/chat/completions")
	if _, ok := other[openCodeSessionHeader]; ok {
		t.Error("non-opencode host should not get a session id")
	}
}

// A user's own header must win, matched case-insensitively so a differently
// capitalised override is still honoured.
func TestResolveHeadersUserWins(t *testing.T) {
	got := resolveHeaders(map[string]string{
		"user-agent":         "my-agent/9",
		"X-OpenCode-Session": "pinned",
	}, "https://opencode.ai/zen/go/v1/responses")
	if v := headerValue(got, "User-Agent"); v != "my-agent/9" {
		t.Errorf("User-Agent = %q, want the user's", v)
	}
	if v := headerValue(got, openCodeSessionHeader); v != "pinned" {
		t.Errorf("session id = %q, want the user's", v)
	}
	if len(got) != 2 {
		t.Errorf("headers = %v, want no duplicates", got)
	}
}

// headerValue looks a header up by name, ignoring capitalisation.
func headerValue(headers map[string]string, name string) string {
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// Configured headers reach the wire alongside the auth and content-type ones.
func TestConfiguredHeadersSent(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		io.WriteString(w, `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)
	}))
	defer srv.Close()

	p, err := CreateClient(ProviderConfig{
		Name: "openai", APIKey: "key", Model: "m", BaseURL: srv.URL,
		Headers: map[string]string{"X-Test": "yes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Completion(context.Background(), CompletionParams{Messages: []Message{{Role: RoleUser, Content: "yo"}}}); err != nil {
		t.Fatal(err)
	}
	if got.Get("X-Test") != "yes" {
		t.Errorf("X-Test = %q", got.Get("X-Test"))
	}
	if got.Get("User-Agent") != defaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", got.Get("User-Agent"), defaultUserAgent)
	}
	if got.Get("Authorization") != "Bearer key" {
		t.Errorf("Authorization = %q", got.Get("Authorization"))
	}
}

// collectStream drains a stream into its text and reasoning, failing on error.
func collectStream(t *testing.T, chunks <-chan ChatCompletionChunk, errs <-chan error) (text, reasoning string) {
	t.Helper()
	var out, think strings.Builder
	for chunk := range chunks {
		for _, choice := range chunk.Choices {
			out.WriteString(choice.Delta.Content)
			if choice.Delta.Reasoning != nil {
				think.WriteString(choice.Delta.Reasoning.Content)
			}
		}
	}
	if err := <-errs; err != nil {
		t.Fatalf("stream error: %v", err)
	}
	return out.String(), think.String()
}
