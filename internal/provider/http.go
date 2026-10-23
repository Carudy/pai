package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The OpenAI-family endpoints PAI speaks all share one request shape: a JSON
// POST with a bearer token. These helpers keep that plumbing in one place so the
// chat-completions and Responses transports cannot drift apart.
//
// Every call is bounded. A provider that accepts the connection and then goes
// silent must not leave the CLI waiting forever: the first byte has a budget, a
// stream may not go quiet mid-answer, and a whole non-streaming completion has an
// outer limit. Transient failures are retried while that is still safe — before
// any response data has been handed to the caller.

const (
	// firstByteTimeout bounds the wait for response headers on a streamed call. It
	// covers connection setup, queueing, and prompt prefill, which for a long
	// context can legitimately take a minute or two; it must still end, or a
	// half-dead connection hangs the run with no way out but Ctrl+C. It does not
	// apply to non-streaming calls: their server sends headers only once the body
	// is ready, so only the outer limit below can bound them.
	firstByteTimeout = 2 * time.Minute
	// streamIdleTimeout bounds silence *within* a stream. SSE providers send a
	// reasoning or content delta regularly; a gap this long means the connection
	// has stalled, and the body is closed so the read returns instead of blocking.
	streamIdleTimeout = 90 * time.Second
	// nonStreamTimeout bounds a whole non-streaming completion. The body only
	// arrives at the end there, so an idle budget would be wrong and only an outer
	// limit can stop a dead call.
	nonStreamTimeout = 10 * time.Minute

	// requestAttempts is how many times one logical request may be sent when the
	// failure happened before any response was delivered (429, 5xx, connection
	// error). A timeout gets fewer attempts: each one already waited 2 minutes.
	requestAttempts = 3
	timeoutAttempts = 2

	// retryMaxWait is the longest Retry-After PAI will sit out. A provider asking
	// for more than this is telling the caller to come back later; waiting half a
	// minute inside a silent terminal is worse than reporting it.
	retryMaxWait = 15 * time.Second
)

// retryBase is a variable so tests can compress the backoff.
var retryBase = 500 * time.Millisecond

// Two clients, because the header budget differs by call shape. SSE sends its
// headers before generation, so a silent stream means a dead connection. A
// non-streaming server usually sends them only once the body is ready, so its
// header wait must not be the streaming budget — the 10-minute context bounds the
// whole call instead.
var (
	streamClient    = newHTTPClient(firstByteTimeout)
	nonStreamClient = newHTTPClient(0)
)

func newHTTPClient(headerTimeout time.Duration) *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{}
	}
	// Clone keeps proxy-from-environment, HTTP/2, keep-alives and the rest of the
	// stdlib defaults; only the budgets change.
	t := base.Clone()
	t.ResponseHeaderTimeout = headerTimeout
	return &http.Client{Transport: t}
}

// ErrStalled reports a stream that stopped delivering data. It is a transport
// failure, not a provider error: the request was well-formed and the response had
// started, then went silent. Callers may retry the whole request when nothing was
// shown to the user yet.
var ErrStalled = errors.New("provider stream stalled")

// HTTPError is a non-2xx response. Status and Retry-After stay typed so retry
// decisions never depend on parsing a message.
type HTTPError struct {
	Provider   string
	Status     int
	Body       string
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("%s: API error (status %d): %s", e.Provider, e.Status, e.Body)
	if e.RetryAfter > 0 {
		msg += fmt.Sprintf(" (retry after %s)", e.RetryAfter.Round(time.Second))
	}
	return msg
}

// postJSON sends body to endpoint and returns the raw response body. A non-2xx
// status becomes an error carrying the response body, which is normally the
// provider's own explanation of what was wrong.
func postJSON(ctx context.Context, name, endpoint, apiKey string, headers map[string]string, body []byte) ([]byte, error) {
	// The whole call, including reading a non-streaming body, must end. Streaming
	// callers take the other path: a long answer is normal there, and only silence
	// is suspicious.
	ctx, cancel := context.WithTimeout(ctx, nonStreamTimeout)
	defer cancel()

	resp, err := postJSONStreamWith(ctx, nonStreamClient, name, endpoint, apiKey, headers, body, 0)
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
// returned body and must close it. idle > 0 makes a silent body an error after
// that long; it is how a stream that stops mid-answer is detected.
func postJSONStream(ctx context.Context, name, endpoint, apiKey string, headers map[string]string, body []byte, idle time.Duration) (*http.Response, error) {
	return postJSONStreamWith(ctx, streamClient, name, endpoint, apiKey, headers, body, idle)
}

func postJSONStreamWith(ctx context.Context, client *http.Client, name, endpoint, apiKey string, headers map[string]string, body []byte, idle time.Duration) (*http.Response, error) {
	var lastErr error
	attempts, tries := requestAttempts, 0
	retried := false
	for attempt := 1; ; attempt++ {
		tries++
		resp, err := postOnce(ctx, client, name, endpoint, apiKey, headers, body)
		if err == nil {
			if idle > 0 {
				resp.Body = newStallGuard(name, resp.Body, idle)
			}
			return resp, nil
		}
		lastErr = err
		if attempt == 1 && isTimeout(err) {
			attempts = timeoutAttempts
		}
		// Cancelled by the caller (Ctrl+C), and errors no retry can fix: stop here.
		if ctx.Err() != nil || !retryable(err) || attempt >= attempts {
			break
		}
		wait, ok := retryWait(attempt, err)
		if !ok {
			break
		}
		retried = true
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	if retried {
		return nil, fmt.Errorf("after %d attempts: %w", tries, lastErr)
	}
	return nil, lastErr
}

func postOnce(ctx context.Context, client *http.Client, name, endpoint, apiKey string, headers map[string]string, body []byte) (*http.Response, error) {
	req, err := newJSONRequest(ctx, name, endpoint, apiKey, headers, body)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
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

// apiError reads and closes a non-2xx response into a typed error. The body read
// is bounded too: an error response that stalls must not recreate the hang it is
// supposed to report.
func apiError(name string, resp *http.Response) error {
	body := readBounded(resp.Body, 8<<10, 5*time.Second)
	resp.Body.Close()
	return &HTTPError{
		Provider:   name,
		Status:     resp.StatusCode,
		Body:       strings.TrimSpace(body),
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}
}

// retryable reports whether sending the request again can help. A 4xx other than
// 429 is the provider rejecting the request itself; transport failures are worth
// another try because nothing was delivered.
func retryable(err error) bool {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status == http.StatusTooManyRequests || he.Status >= 500
	}
	return true
}

// retryWait returns how long to wait before the next attempt. ok=false means the
// provider asked for a longer pause than PAI is willing to sit through, so the
// error is reported instead.
func retryWait(attempt int, err error) (time.Duration, bool) {
	var he *HTTPError
	if errors.As(err, &he) && he.RetryAfter > 0 {
		if he.RetryAfter > retryMaxWait {
			return 0, false
		}
		return he.RetryAfter, true
	}
	d := retryBase << (attempt - 1)
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d, true
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}

// readBounded reads at most limit bytes, giving up after d so a stalled error
// body cannot block the report.
func readBounded(r io.ReadCloser, limit int64, d time.Duration) string {
	var once sync.Once
	timer := time.AfterFunc(d, func() { once.Do(func() { _ = r.Close() }) })
	defer timer.Stop()
	data, _ := io.ReadAll(io.LimitReader(r, limit))
	return string(data)
}

// stallGuard turns silence on a stream into an error. A blocking Read cannot be
// interrupted directly, so the timer closes the body, which unblocks it; the
// resulting error is reported as ErrStalled rather than as a mysterious
// "connection closed".
type stallGuard struct {
	name    string
	body    io.ReadCloser
	timeout time.Duration
	timer   *time.Timer
	stalled atomic.Bool
}

func newStallGuard(name string, body io.ReadCloser, timeout time.Duration) *stallGuard {
	g := &stallGuard{name: name, body: body, timeout: timeout}
	g.timer = time.AfterFunc(timeout, func() {
		g.stalled.Store(true)
		_ = g.body.Close()
	})
	return g
}

func (g *stallGuard) Read(p []byte) (int, error) {
	n, err := g.body.Read(p)
	if n > 0 {
		g.timer.Reset(g.timeout)
	}
	if err != nil && g.stalled.Load() {
		return n, fmt.Errorf("%s: %w: no data for %s; the request was cancelled (send the instruction again to retry): %v",
			g.name, ErrStalled, g.timeout.Round(time.Second), err)
	}
	return n, err
}

func (g *stallGuard) Close() error {
	g.timer.Stop()
	return g.body.Close()
}
