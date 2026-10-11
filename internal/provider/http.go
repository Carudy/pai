package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
)

// The OpenAI-family endpoints PAI speaks all share one request shape: a JSON
// POST with a bearer token. These helpers keep that plumbing in one place so the
// chat-completions and Responses transports cannot drift apart.

// postJSON sends body to endpoint and returns the raw response body. A non-2xx
// status becomes an error carrying the response body, which is normally the
// provider's own explanation of what was wrong.
func postJSON(ctx context.Context, name, endpoint, apiKey string, headers map[string]string, body []byte) ([]byte, error) {
	resp, err := postJSONStream(ctx, name, endpoint, apiKey, headers, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: read response: %w", name, err)
	}
	return raw, nil
}

// postJSONStream is postJSON for a streaming request: the caller owns the
// returned body and must close it.
func postJSONStream(ctx context.Context, name, endpoint, apiKey string, headers map[string]string, body []byte) (*http.Response, error) {
	req, err := newJSONRequest(ctx, name, endpoint, apiKey, headers, body)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: do request: %w", name, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(name, resp)
	}
	return resp, nil
}

func newJSONRequest(ctx context.Context, name, endpoint, apiKey string, headers map[string]string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: create request: %w", name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// apiError reads and closes a non-2xx response into a formatted error.
func apiError(name string, resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return fmt.Errorf("%s: API error (status %d): %s", name, resp.StatusCode, string(body))
}
