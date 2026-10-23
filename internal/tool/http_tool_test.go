package tool

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExecuteHTTPGet(t *testing.T) {
	var gotMethod, gotQuery, gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.RawQuery
		gotHeader = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	var streamed strings.Builder
	result, err := ExecuteHTTP(context.Background(), HTTPRequest{
		Method: "GET",
		URL:    server.URL + "/search?q=a%2Fb&n=5",
		Header: map[string]string{"Accept": "application/json"},
	}, &streamed)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" || gotQuery != "q=a%2Fb&n=5" || gotHeader != "application/json" {
		t.Fatalf("request = (%s, %s, %s)", gotMethod, gotQuery, gotHeader)
	}
	if result.Status != http.StatusOK || string(result.Body) != `{"ok":true}` || result.Truncated {
		t.Fatalf("result = %+v", result)
	}
	if streamed.String() != `{"ok":true}` {
		t.Fatalf("streamed = %q", streamed.String())
	}
}

func TestExecuteHTTPPostBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || string(body) != `{"q":"x"}` {
			t.Errorf("request = (%s, %s)", r.Method, body)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		_, _ = io.WriteString(w, "done")
	}))
	defer server.Close()

	result, err := ExecuteHTTP(context.Background(), HTTPRequest{
		Method: "POST",
		URL:    server.URL + "/items",
		Header: map[string]string{"Content-Type": "application/json"},
		Body:   []byte(`{"q":"x"}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != http.StatusOK || string(result.Body) != "done" {
		t.Fatalf("result = %+v", result)
	}
}

func TestExecuteHTTPCapsBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 100))
	}))
	defer server.Close()

	result, err := ExecuteHTTP(context.Background(), HTTPRequest{
		Method: "GET", URL: server.URL, MaxBytes: 10,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(result.Body) != 10 {
		t.Fatalf("result = %+v, want a 10-byte truncated body", result)
	}
}

func TestExecuteHTTPNon2xxIsAResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer server.Close()

	result, err := ExecuteHTTP(context.Background(), HTTPRequest{Method: "GET", URL: server.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != http.StatusForbidden || !strings.Contains(string(result.Body), "nope") {
		t.Fatalf("result = %+v", result)
	}
}

func TestExecuteHTTPTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "late")
	}))
	defer server.Close()

	result, err := ExecuteHTTP(context.Background(), HTTPRequest{
		Method: "GET", URL: server.URL, Timeout: 20 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("a timeout must be a result, not an error: %v", err)
	}
	if !result.TimedOut {
		t.Fatalf("result = %+v, want TimedOut", result)
	}
}

func TestExecuteHTTPRefusesRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer server.Close()

	_, err := ExecuteHTTP(context.Background(), HTTPRequest{Method: "GET", URL: server.URL}, nil)
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("err = %v, want a redirect refusal", err)
	}
}

func TestFormatHTTPBody(t *testing.T) {
	pretty := FormatHTTPBody([]byte(`{"a":1,"b":[2,3]}`), "", 1024)
	if !strings.Contains(pretty, "\n  \"a\": 1") {
		t.Fatalf("json was not indented:\n%s", pretty)
	}
	if got := FormatHTTPBody([]byte("plain <xml/>"), "", 1024); got != "plain <xml/>" {
		t.Fatalf("non-JSON body changed: %q", got)
	}
	if got := FormatHTTPBody([]byte(`{"a":1}`), "text", 1024); got != `{"a":1}` {
		t.Fatalf("format=text changed the body: %q", got)
	}
	capped := FormatHTTPBody([]byte(strings.Repeat("y", 64)), "text", 8)
	if !strings.HasPrefix(capped, "yyyyyyyy") || !strings.Contains(capped, "truncated at 8 bytes") {
		t.Fatalf("capped = %q", capped)
	}
}

func TestCutUTF8DoesNotSplitARune(t *testing.T) {
	// "é" is two bytes; a 1-byte cut must drop it rather than emit half.
	if got := cutUTF8("é", 1); got != "" {
		t.Fatalf("cutUTF8 = %q, want empty", got)
	}
	if got := cutUTF8("aé", 2); got != "a" {
		t.Fatalf("cutUTF8 = %q, want %q", got, "a")
	}
}
