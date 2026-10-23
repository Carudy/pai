package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fastRetries keeps backoff out of the test's critical path.
func fastRetries(t *testing.T) {
	t.Helper()
	old := retryBase
	retryBase = time.Millisecond
	t.Cleanup(func() { retryBase = old })
}

// A provider that is briefly unavailable should be retried, invisibly.
func TestRetriesTransientStatus(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	body, err := postJSON(context.Background(), "test", srv.URL, "k", nil, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("body = %q", body)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

// A rejected credential is the caller's problem; retrying only delays the report.
func TestDoesNotRetryClientErrors(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := postJSON(context.Background(), "test", srv.URL, "k", nil, []byte(`{}`))
	if err == nil {
		t.Fatal("expected an error")
	}
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusUnauthorized {
		t.Fatalf("error = %v, want a typed 401", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
}

// Retry-After is honoured, but a pause longer than the budget is reported rather
// than sat through.
func TestRetryAfterLongerThanBudgetIsReported(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "60")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := postJSON(context.Background(), "test", srv.URL, "k", nil, []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "retry after") {
		t.Fatalf("error = %v, want the Retry-After hint", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1 (no long wait)", got)
	}
}

// A stream that goes silent after its headers must fail instead of blocking
// forever. This is the case a user hit as "it just keeps waiting".
func TestStreamStallIsDetected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // hold the connection open, sending nothing
	}))
	defer srv.Close()

	resp, err := postJSONStream(context.Background(), "test", srv.URL, "k", nil, []byte(`{}`), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); !errors.Is(err, ErrStalled) {
		t.Fatalf("read error = %v, want ErrStalled", err)
	}
}

// A stream that keeps delivering data must not trip the idle guard.
func TestStreamThatKeepsTalkingIsNotStalled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for range 6 {
			_, _ = io.WriteString(w, "data: {}\n\n")
			flusher.Flush()
			time.Sleep(15 * time.Millisecond)
		}
	}))
	defer srv.Close()

	resp, err := postJSONStream(context.Background(), "test", srv.URL, "k", nil, []byte(`{}`), 80*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("a talking stream was cut off: %v", err)
	}
}

// A non-streaming call has an outer bound too: the body only arrives at the end,
// so the wait is bounded by the context rather than by silence.
func TestPostHonoursContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := postJSON(ctx, "test", srv.URL, "k", nil, []byte(`{}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the context deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("waited %s for a cancelled request", elapsed)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("7"); got != 7*time.Second {
		t.Errorf("seconds = %v", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("empty = %v", got)
	}
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got < 20*time.Second || got > 31*time.Second {
		t.Errorf("http date = %v, want about 30s", got)
	}
	past := time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Errorf("past date = %v, want 0", got)
	}
}
