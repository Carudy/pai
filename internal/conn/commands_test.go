package conn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/runner"
)

// safeBuffer is written by the stream goroutine and read by the test.
type safeBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *safeBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *safeBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// recorder is an attach wired to a stub server that remembers requests.
type recorder struct {
	mu       sync.Mutex
	requests []recorded
	respond  func(w http.ResponseWriter, r *http.Request) bool
}

type recorded struct {
	method, path, body string
}

func (s *recorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, recorded{r.Method, r.URL.RequestURI(), string(data)})
		s.mu.Unlock()
		if s.respond != nil && s.respond(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/models":
			fmt.Fprint(w, `{"models":["a:one","b:two"],"default_model":"a:one"}`)
		case "/api/roles":
			fmt.Fprint(w, `{"roles":["default","worker"],"default_role":"default"}`)
		case "/api/trust-all":
			fmt.Fprint(w, `{"trust_all":true}`)
		case "/api/sessions":
			fmt.Fprint(w, `{"total":1,"sessions":[{"name":"demo","role":"default","model":"a:one","cwd":"/w"}]}`)
		case "/api/context":
			fmt.Fprint(w, `{"model":"a:one","total":2,"offset":0,"compact_turns":1,"config":{"keep_turns":4},"cards":[`+
				`{"seq":1,"role":"user","kind":"input","original":"first","sent":"first","state":"sent"},`+
				`{"seq":2,"role":"assistant","kind":"output","original":"second","sent":"","state":"elided","essential":true}]}`)
		case "/api/history":
			fmt.Fprint(w, `{"name":"demo","role":"default","model":"a:one","cwd":"/w","total":3,"turns":[`+
				`{"Role":"user","Kind":"input","Content":"earlier question","Seq":1,"Essential":true},`+
				`{"Role":"assistant","Kind":"output","Content":"{\"action\":\"tool\",\"toolname\":\"read\",\"payload\":{\"path\":\"x\"},\"reason\":\"\"}","Seq":2},`+
				`{"Role":"assistant","Kind":"output","Content":"{\"action\":\"done\",\"payload\":\"the answer\",\"reason\":\"done\"}","Seq":3}]}`)
		default:
			fmt.Fprint(w, `{"ok":true}`)
		}
	})
}

func (s *recorder) last() recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return recorded{}
	}
	return s.requests[len(s.requests)-1]
}

func (s *recorder) find(method, path string) (recorded, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.requests) - 1; i >= 0; i-- {
		if r := s.requests[i]; r.method == method && strings.HasPrefix(r.path, path) {
			return r, true
		}
	}
	return recorded{}, false
}

func (s *recorder) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func newRecorder(t *testing.T) (*recorder, *attach, *safeBuffer, *fakeObserver) {
	t.Helper()
	s := &recorder{}
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	out := &safeBuffer{}
	obs := &fakeObserver{}
	a := &attach{client: New(srv.URL, "secret"), name: "demo", obs: obs, out: out}
	return s, a, out, obs
}

func TestLocalSwitchCommands(t *testing.T) {
	s, a, out, _ := newRecorder(t)
	ctx := context.Background()

	a.local(ctx, "/trust")
	if !strings.Contains(out.String(), "trust-all: true") {
		t.Errorf("read trust: %s", out.String())
	}
	a.local(ctx, "/trust off")
	if r := s.last(); r.method != "POST" || !strings.Contains(r.body, `"trust_all":false`) {
		t.Errorf("set trust = %+v", r)
	}
	before := s.n()
	a.local(ctx, "/trust onn")
	if s.n() != before || !strings.Contains(out.String(), "usage: /trust") {
		t.Errorf("a typo must not change auto-approval: %s", out.String())
	}

	a.local(ctx, "/model")
	if r := s.last(); r.path != "/api/models" {
		t.Errorf("list models = %+v", r)
	}
	a.local(ctx, "/model b:two")
	if r := s.last(); r.path != "/api/model" || !strings.Contains(r.body, `"model":"b:two"`) {
		t.Errorf("set model = %+v", r)
	}
	if a.meta.Model != "b:two" {
		t.Errorf("local meta not updated: %+v", a.meta)
	}

	a.local(ctx, "/role")
	if r := s.last(); r.path != "/api/roles" {
		t.Errorf("list roles = %+v", r)
	}
	a.local(ctx, "/role worker")
	if r := s.last(); r.path != "/api/role" || !strings.Contains(r.body, `"role":"worker"`) {
		t.Errorf("set role = %+v", r)
	}

	a.local(ctx, "/dir")
	a.local(ctx, "/dir /tmp/x")
	if r := s.last(); r.path != "/api/working-dir" || !strings.Contains(r.body, `"working_dir":"/tmp/x"`) {
		t.Errorf("set dir = %+v", r)
	}
	if a.meta.Cwd != "/tmp/x" {
		t.Errorf("cwd not updated: %+v", a.meta)
	}
}

func TestLocalListCommands(t *testing.T) {
	s, a, out, _ := newRecorder(t)
	ctx := context.Background()

	a.local(ctx, "/sessions")
	text := out.String()
	if r := s.last(); !strings.HasPrefix(r.path, "/api/sessions") {
		t.Errorf("sessions path = %+v", r)
	}
	if !strings.Contains(text, "* demo") {
		t.Errorf("current session not marked: %s", text)
	}

	a.local(ctx, "/inspect 5")
	text = out.String()
	if r, ok := s.find("GET", "/api/context"); !ok || !strings.Contains(r.path, "limit=5") {
		t.Errorf("inspect fetch = %+v", r)
	}
	for _, want := range []string{"context: a:one", "#1", "#2", "★"} {
		if !strings.Contains(text, want) {
			t.Errorf("inspect output missing %q:\n%s", want, text)
		}
	}
}

func TestLocalForwardsUnknownAndSteers(t *testing.T) {
	s, a, out, _ := newRecorder(t)
	ctx := context.Background()

	a.local(ctx, "/compact")
	if r := s.last(); r.path != "/api/send" || !strings.Contains(r.body, `"text":"/compact"`) {
		t.Errorf("unknown command was not forwarded: %+v", r)
	}
	a.local(ctx, "/steer stop now")
	if r := s.last(); r.path != "/api/steer" || !strings.Contains(r.body, `"text":"stop now"`) {
		t.Errorf("steer = %+v", r)
	}
	if !strings.Contains(out.String(), "steering: stop now") {
		t.Errorf("steer not acknowledged: %s", out.String())
	}
	a.local(ctx, "/steer")
	if !strings.Contains(out.String(), "usage: /steer") {
		t.Errorf("missing steer usage: %s", out.String())
	}
}

func TestLoopSendsLiteralSlashAndAnswersPrompt(t *testing.T) {
	s, a, _, _ := newRecorder(t)
	// A prompt that is pending before the first line must consume the next answer.
	a.setPrompt(&runner.Prompt{ID: "p1", Kind: "confirm", Title: "Run?"})

	in := strings.NewReader("y\n//plain\nhello\n")
	if err := a.loop(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	requests := append([]recorded(nil), s.requests...)
	s.mu.Unlock()

	var replies, sends []recorded
	for _, r := range requests {
		switch r.path {
		case "/api/reply":
			replies = append(replies, r)
		case "/api/send":
			sends = append(sends, r)
		}
	}
	if len(replies) != 1 || !strings.Contains(replies[0].body, `"choice":"once"`) {
		t.Fatalf("replies = %+v", replies)
	}
	if len(sends) != 2 {
		t.Fatalf("sends = %+v", sends)
	}
	if !strings.Contains(sends[0].body, `"text":"/plain"`) {
		t.Errorf("// prefix was not collapsed: %s", sends[0].body)
	}
	if !strings.Contains(sends[1].body, `"text":"hello"`) {
		t.Errorf("plain instruction = %s", sends[1].body)
	}
}

func TestStatusErrorClassification(t *testing.T) {
	notFound := &StatusError{Code: http.StatusNotFound}
	if !isNotFound(notFound) || !retryable(notFound) {
		t.Error("a missing worker must be retryable")
	}
	auth := &StatusError{Code: http.StatusUnauthorized}
	if isNotFound(auth) || retryable(auth) {
		t.Error("a rejected credential must not be retried")
	}
	if !retryable(errors.New("connection refused")) {
		t.Error("a transport failure is retryable")
	}
}

func TestStreamPollsAnIdleSession(t *testing.T) {
	var (
		mu     sync.Mutex
		probes int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		probes++
		n := probes
		mu.Unlock()
		if n == 1 {
			http.Error(w, runner.ErrNotFound.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"done\",\"data\":\"ok\",\"snapshot\":{}}\n\n")
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []Event
	_ = New(srv.URL, "").Stream(ctx, "demo", func(e Event) {
		got = append(got, e)
		cancel()
	})
	if len(got) != 1 || got[0].Type != "done" {
		t.Fatalf("after idle poll, events = %+v", got)
	}
}

func TestStreamStopsOnRejectedCredential(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	obs := &fakeObserver{}
	var a attach
	a.obs = obs
	err := New(srv.URL, "bad").Stream(ctx, "demo", a.handle)
	if err == nil || isNotFound(err) || retryable(err) {
		t.Fatalf("stream error = %v", err)
	}
	if hits != 1 {
		t.Fatalf("retried a rejected credential: %d requests", hits)
	}
	if len(obs.calls) != 1 || !strings.HasPrefix(obs.calls[0], "notice:error: ") {
		t.Fatalf("observer calls = %v", obs.calls)
	}
}

func TestRunReplaysAnIdleSession(t *testing.T) {
	rec, a, out, obs := newRecorder(t)
	rec.respond = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/api/events", "/api/snapshot":
			http.Error(w, runner.ErrNotFound.Error(), http.StatusNotFound)
			return true
		}
		return false
	}

	err := Run(context.Background(), Options{
		Client:   a.client,
		Session:  "demo",
		In:       strings.NewReader(""), // EOF detaches immediately
		Out:      out,
		Observer: obs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "attached to demo") {
		t.Errorf("banner missing: %s", out.String())
	}
	wantCalls := "user:★ earlier question,done:the answer"
	if got := strings.Join(obs.calls, ","); got != wantCalls {
		t.Errorf("replay calls = %q, want %q\ntool calls and plain instructions are noise here", got, wantCalls)
	}
	if !strings.Contains(out.String(), "end of stored transcript") {
		t.Errorf("replay footer missing: %s", out.String())
	}
	if r, _ := rec.find("GET", "/api/history"); !strings.Contains(r.path, "limit=80") {
		t.Errorf("replay window = %+v", r)
	}
}

func TestRunReplaysAnAwaitingWorker(t *testing.T) {
	rec, a, _, obs := newRecorder(t)
	rec.respond = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/api/snapshot":
			fmt.Fprint(w, `{"name":"demo","state":"awaiting","queued":0}`)
			return true
		case "/api/events":
			http.Error(w, runner.ErrNotFound.Error(), http.StatusNotFound)
			return true
		}
		return false
	}
	out := &safeBuffer{}
	err := Run(context.Background(), Options{
		Client: a.client, Session: "demo", In: strings.NewReader(""), Out: out, Observer: obs,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Nothing is in flight, so the stored transcript is shown even though a
	// worker exists.
	if got := strings.Join(obs.calls, ","); got != "user:★ earlier question,done:the answer" {
		t.Fatalf("replay calls = %q", got)
	}
}

func TestRunDoesNotReplayBusyWork(t *testing.T) {
	rec, a, out, obs := newRecorder(t)
	rec.respond = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/api/snapshot":
			fmt.Fprint(w, `{"name":"demo","state":"busy","queued":1}`)
			return true
		case "/api/events":
			http.Error(w, runner.ErrNotFound.Error(), http.StatusNotFound)
			return true
		}
		return false
	}
	err := Run(context.Background(), Options{
		Client: a.client, Session: "demo", In: strings.NewReader(""), Out: out, Observer: obs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.calls) != 0 {
		t.Errorf("busy attach replayed: %v", obs.calls)
	}
	if strings.Contains(out.String(), "stored transcript") {
		t.Errorf("busy attach reprinted history: %s", out.String())
	}
}

func TestRunReportsRetainedFailure(t *testing.T) {
	rec, a, _, obs := newRecorder(t)
	rec.respond = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/api/snapshot":
			fmt.Fprint(w, `{"name":"demo","state":"stopped","error":"no API key"}`)
			return true
		case "/api/events":
			http.Error(w, runner.ErrNotFound.Error(), http.StatusNotFound)
			return true
		}
		return false
	}
	err := Run(context.Background(), Options{
		Client: a.client, Session: "demo", In: strings.NewReader(""), Out: io.Discard, Observer: obs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.calls) != 1 || !strings.Contains(obs.calls[0], "previous run failed: no API key") {
		t.Fatalf("calls = %v", obs.calls)
	}
}

func TestRunRejectsUnknownSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "session not found", http.StatusNotFound)
	}))
	defer srv.Close()

	err := Run(context.Background(), Options{
		Client:   New(srv.URL, ""),
		Session:  "missing",
		In:       strings.NewReader(""),
		Out:      io.Discard,
		Observer: &fakeObserver{},
	})
	if err == nil || !strings.Contains(err.Error(), `session "missing"`) {
		t.Fatalf("error = %v", err)
	}
}

// A done payload that is not a plain string must still reach the observer.
func TestHandleShowsStructuredPayloads(t *testing.T) {
	obs := &fakeObserver{}
	a := &attach{obs: obs, out: io.Discard}
	a.handle(Event{Type: "done", Data: json.RawMessage(`{"answer":"ok"}`)})
	if len(obs.calls) != 1 || obs.calls[0] != `done:{"answer":"ok"}` {
		t.Fatalf("calls = %v", obs.calls)
	}
}
