package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const tavilyURL = "https://api.tavily.com/search"

// SearchItem is a single result from a web search.
type SearchItem struct {
	URL     string  `json:"url"`
	Title   string  `json:"title"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// SearchResult holds the full response from the search API.
type SearchResult struct {
	Query        string       `json:"query"`
	Answer       string       `json:"answer"`
	Results      []SearchItem `json:"results"`
	ResponseTime float64      `json:"response_time"`
}

// Search calls the Tavily search API and returns structured results.
// apiKey is the preferred key (from config); if empty, $TAVILY_API_KEY is used.
func Search(ctx context.Context, query, apiKey string) (*SearchResult, error) {
	if apiKey == "" {
		apiKey = os.Getenv("TAVILY_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("TAVILY_API_KEY not set (config or environment)")
	}

	body := map[string]any{
		"query":          query,
		"search_depth":   "basic",
		"include_answer": true,
		"max_results":    3,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilyURL, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("search API status %d: %s", resp.StatusCode, string(msg))
	}

	var sr SearchResult
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	sr.Query = query
	return &sr, nil
}

// Format returns a markdown-like string suitable as agent context.  If the
// API returned an AI-generated answer it is placed first, followed by the
// numbered search results.
func (sr *SearchResult) Format() string {
	var b strings.Builder

	if sr.Answer != "" {
		b.WriteString("AI Answer: ")
		b.WriteString(sr.Answer)
		b.WriteString("\n\n---\n\n")
	}

	b.WriteString("Web search results for: ")
	b.WriteString(sr.Query)
	b.WriteString("\n\n")

	for i, r := range sr.Results {
		fmt.Fprintf(&b, "%d. %s (score: %.2f)\n", i+1, r.Title, r.Score)
		fmt.Fprintf(&b, "   URL: %s\n", r.URL)
		if r.Content != "" {
			// Keep content concise for agent context.
			content := strings.TrimSpace(r.Content)
			if len(content) > 500 {
				content = content[:500] + "…"
			}
			fmt.Fprintf(&b, "   %s\n", content)
		}
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n")
}
