package conn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/runner"
)

type fakeObserver struct{ calls []string }

func (o *fakeObserver) Reason(s string)     { o.calls = append(o.calls, "reason:"+s) }
func (o *fakeObserver) Done(s string)       { o.calls = append(o.calls, "done:"+s) }
func (o *fakeObserver) Terminate(s string)  { o.calls = append(o.calls, "terminate:"+s) }
func (o *fakeObserver) Ask(s string)        { o.calls = append(o.calls, "ask:"+s) }
func (o *fakeObserver) Awaiting()           { o.calls = append(o.calls, "awaiting") }
func (o *fakeObserver) User(s string)       { o.calls = append(o.calls, "user:"+s) }
func (o *fakeObserver) Session(s string)    { o.calls = append(o.calls, "session:"+s) }
func (o *fakeObserver) Reasoning(s string)  { o.calls = append(o.calls, "reasoning:"+s) }
func (o *fakeObserver) Output(s string)     { o.calls = append(o.calls, "output:"+s) }
func (o *fakeObserver) Notice(s string)     { o.calls = append(o.calls, "notice:"+s) }
func (o *fakeObserver) Separator()          { o.calls = append(o.calls, "separator") }
func (o *fakeObserver) Compacting()         { o.calls = append(o.calls, "compacting") }
func (o *fakeObserver) ToolOutput(s string) { o.calls = append(o.calls, "toolout:"+s) }
func (o *fakeObserver) Usage(u core.Usage) {
	o.calls = append(o.calls, fmt.Sprintf("usage:%d", u.Total))
}
func (o *fakeObserver) ToolCall(c core.ToolCall) {
	o.calls = append(o.calls, "tool:"+c.Name+":"+c.Detail)
}
func (o *fakeObserver) ToolResult(r core.ToolResult) {
	o.calls = append(o.calls, fmt.Sprintf("result:%t:%s", r.OK, r.Message))
}

var _ core.Observer = (*fakeObserver)(nil)

func TestHandleMapsEventsToObserver(t *testing.T) {
	obs := &fakeObserver{}
	a := &attach{obs: obs, out: io.Discard}
	for _, ev := range []Event{
		{Type: "tool_call", Data: json.RawMessage(`{"Name":"execute","Detail":"ls"}`)},
		{Type: "tool_output", Data: json.RawMessage(`"out"`)},
		{Type: "tool_result", Data: json.RawMessage(`{"OK":true,"Message":"done"}`)},
		{Type: "done", Data: json.RawMessage(`"finished"`)},
		{Type: "separator"},
	} {
		a.handle(ev)
	}
	want := "tool:execute:ls,toolout:out,result:true:done,done:finished,separator"
	if got := strings.Join(obs.calls, ","); got != want {
		t.Fatalf("observer calls = %q, want %q", got, want)
	}
}

func TestPromptTextAndAnswer(t *testing.T) {
	p := &runner.Prompt{Kind: "confirm", Title: "Execute this command?", Tool: &core.ToolCall{Detail: "rm x"}, Untrusted: []string{"rm"}}
	text := promptText(p)
	for _, want := range []string{"Execute this command?", "rm x", "untrusted: rm", "Approve?"} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt text missing %q: %s", want, text)
		}
	}
	for line, want := range map[string]string{"y": "once", "s": "session", "a": "always", "n": "deny", "junk": "deny"} {
		if _, got := answer(p, line); got != want {
			t.Errorf("answer(%q) = %q, want %q", line, got, want)
		}
	}
	ask := &runner.Prompt{Kind: "ask", Title: "Which file?"}
	if body, _ := answer(ask, "main.go"); body != "main.go" {
		t.Errorf("ask answer = %q, want the typed line", body)
	}
}

func TestClientSendsBearerJSON(t *testing.T) {
	var path, auth, origin, method, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth, origin, method = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Origin"), r.Method
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	if err := New(srv.URL, "secret").Send(context.Background(), "one", "hello"); err != nil {
		t.Fatal(err)
	}
	if path != "/api/send" || auth != "Bearer secret" {
		t.Errorf("request = %s auth=%q", path, auth)
	}
	// A terminal has no browser origin, so it claims the address it talks to;
	// the server's mutation gate needs a bearer token or a matching origin.
	if origin != srv.URL {
		t.Errorf("origin = %q, want %q", origin, srv.URL)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if decoded["name"] != "one" || decoded["text"] != "hello" {
		t.Errorf("body = %v", decoded)
	}

	// Reads must stay origin-free: the header exists only for the gate.
	if err := New(srv.URL, "").Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet || origin != "" {
		t.Errorf("GET carried %s origin %q", method, origin)
	}
}

func TestStreamParsesDataFrames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": heartbeat\n\n")
		fmt.Fprint(w, "data: {\"type\":\"done\",\"data\":\"ok\",\"snapshot\":{}}\n\n")
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var got []Event
	_ = New(srv.URL, "").Stream(ctx, "one", func(e Event) {
		got = append(got, e)
		cancel()
	})
	if len(got) != 1 || got[0].Type != "done" || string(got[0].Data) != `"ok"` {
		t.Fatalf("stream events = %+v", got)
	}
}
