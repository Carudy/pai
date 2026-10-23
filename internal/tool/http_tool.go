package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// The definition owns the destination, so a request cannot be redirected to a
// different host after approval. The budgets mirror the provider's rules: a
// bounded wait for headers and an outer bound for a whole call, on top of the
// step context that Ctrl+C cancels. `tool` stays a leaf, so the constants are
// repeated here rather than imported from `provider`.
const (
	defaultHTTPToolBytes  = 32 << 10
	httpToolHeaderTimeout = 2 * time.Minute
	httpToolTotalTimeout  = 10 * time.Minute
	httpToolUserAgent     = "pai-tool/1.0"
)

// HTTPRequest is one definition-owned HTTP call. Method, URL, headers, and body
// are already rendered from validated parameters; a parameter never chooses the
// destination. Header values may contain a secret resolved from the
// environment, so callers must not log this struct.
type HTTPRequest struct {
	Method   string
	URL      string
	Header   map[string]string
	Body     []byte
	MaxBytes int
	Timeout  time.Duration
}

// HTTPResult is the bounded outcome. A non-2xx response is still a result; a
// timeout is reported through TimedOut so the caller can word it as an
// observation rather than a transport error.
type HTTPResult struct {
	Status    int
	Body      []byte
	TimedOut  bool
	Truncated bool
}

// httpToolClient is shared: redirects would defeat the literal-host rule, and a
// silent server must not hold the loop forever.
var httpToolClient = newHTTPToolClient()

func newHTTPToolClient() *http.Client {
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("redirects are refused")
		},
	}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport := base.Clone()
		transport.ResponseHeaderTimeout = httpToolHeaderTimeout
		client.Transport = transport
	}
	return client
}

// ExecuteHTTP performs one bounded request. The body is capped at MaxBytes
// (32 KiB when unset); the cap is reported through Truncated. Cancellation is
// the caller's context; a deadline is returned as TimedOut with any partial
// body, not as an error.
func ExecuteHTTP(ctx context.Context, spec HTTPRequest, streamW io.Writer) (HTTPResult, error) {
	if strings.TrimSpace(spec.URL) == "" {
		return HTTPResult{}, fmt.Errorf("http url must not be empty")
	}
	limit := spec.MaxBytes
	if limit <= 0 {
		limit = defaultHTTPToolBytes
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = httpToolTotalTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if len(spec.Body) > 0 {
		body = bytes.NewReader(spec.Body)
	}
	req, err := http.NewRequestWithContext(ctx, spec.Method, spec.URL, body)
	if err != nil {
		return HTTPResult{}, fmt.Errorf("build request: %w", err)
	}
	for key, value := range spec.Header {
		req.Header.Set(key, value)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", httpToolUserAgent)
	}

	resp, err := httpToolClient.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return HTTPResult{TimedOut: true}, nil
		}
		if ctx.Err() != nil {
			return HTTPResult{}, ctx.Err()
		}
		return HTTPResult{}, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	n, readErr := io.Copy(&buf, io.LimitReader(resp.Body, int64(limit)+1))
	result := HTTPResult{Status: resp.StatusCode, Body: buf.Bytes()}
	if n > int64(limit) {
		result.Body = result.Body[:limit]
		result.Truncated = true
	}
	if streamW != nil && len(result.Body) > 0 {
		_, _ = streamW.Write(result.Body)
	}
	if readErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result.TimedOut = true
			return result, nil
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, fmt.Errorf("read response: %w", readErr)
	}
	return result, nil
}

// FormatHTTPBody renders a capped response for the model. "json" (the default)
// pretty-prints a valid JSON document and leaves anything else untouched, so an
// XML or text endpoint still works. Indentation can expand the body, so the cap
// is applied again after formatting.
func FormatHTTPBody(body []byte, format string, limit int) string {
	if limit <= 0 {
		limit = defaultHTTPToolBytes
	}
	text := string(body)
	if !strings.EqualFold(format, "text") {
		var indented bytes.Buffer
		if err := json.Indent(&indented, body, "", "  "); err == nil {
			text = indented.String()
		}
	}
	if len(text) <= limit {
		return text
	}
	return cutUTF8(text, limit) + fmt.Sprintf("\n[response truncated at %d bytes]", limit)
}

func cutUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	b := []byte(s[:n])
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b)
}
