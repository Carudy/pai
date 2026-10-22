// Package conn attaches to a running `pai serve` over its HTTP/SSE API.
//
// It is a second transport adapter, alongside the browser UI: events arrive as
// the same runner.Event values and are rendered through core.Observer, so the
// terminal renderer is reused unchanged. It never opens the session store, which
// is what lets it run while the server owns the store's exclusive lock.
package conn

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/runner"
)

// DefaultURL is where `pai serve` listens unless told otherwise.
const DefaultURL = "http://127.0.0.1:9384"

const (
	reconnectMin = 300 * time.Millisecond
	reconnectMax = 5 * time.Second
	// idlePoll is the pause between probes of a session with no live run. A
	// subscription exists only while a worker does, so an idle session answers
	// 404; polling at a steady short interval means the first instruction
	// attaches within a second instead of waiting out an exponential backoff.
	idlePoll = time.Second
)

// Client is a minimal HTTP/SSE client for pai serve.
type Client struct {
	BaseURL string
	Token   string
	// Origin is sent on mutating requests. The server accepts a mutation from a
	// same-origin browser or from a bearer-authenticated client; a terminal has no
	// browser origin, so it claims the address it is already talking to. Override
	// it when the server is configured with a different --public-origin.
	Origin string
	HTTP   *http.Client
}

// StatusError is a non-2xx answer from pai serve. The code is kept typed so a
// caller can tell an idle session (404) from a rejected credential, which must
// not be retried.
type StatusError struct {
	Code   int
	Method string
	Path   string
	Body   string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("pai serve %s %s: %d", e.Method, e.Path, e.Code)
	if e.Body != "" {
		msg += " " + e.Body
	}
	return msg
}

// isNotFound reports the normal "no live run for this session" answer.
func isNotFound(err error) bool {
	var s *StatusError
	return errors.As(err, &s) && s.Code == http.StatusNotFound
}

// retryable reports whether another attempt can help. A rejected credential or a
// malformed request cannot, so retrying would only hide the real problem.
func retryable(err error) bool {
	var s *StatusError
	if !errors.As(err, &s) {
		return true // transport failure: the server may come back
	}
	switch s.Code {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusMethodNotAllowed:
		return false
	}
	return true
}

// New returns a client; an empty baseURL means DefaultURL.
func New(baseURL, token string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultURL
	}
	c := &Client{BaseURL: baseURL, Token: token, HTTP: &http.Client{}}
	if u, err := url.Parse(baseURL); err == nil && u.Scheme != "" && u.Host != "" {
		c.Origin = u.Scheme + "://" + u.Host
	}
	return c
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, payload)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if method != http.MethodGet && c.Origin != "" {
		req.Header.Set("Origin", c.Origin)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, &StatusError{Code: resp.StatusCode, Method: method, Path: path, Body: strings.TrimSpace(string(data))}
	}
	return resp, nil
}

// Ping checks reachability and credentials.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/api/sessions?offset=0&limit=1", nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// Sessions lists known sessions.
func (c *Client) Sessions(ctx context.Context, offset, limit int) ([]runner.Meta, int, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/sessions?offset=%d&limit=%d", offset, limit), nil)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	var out struct {
		Sessions []runner.Meta `json:"sessions"`
		Total    int           `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, 0, err
	}
	return out.Sessions, out.Total, nil
}

// History returns a window of a session's stored turns and its metadata. It is
// also the existence check: a missing session surfaces as an error.
func (c *Client) History(ctx context.Context, name string, offset, limit int) (runner.History, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/history?name=%s&offset=%d&limit=%d", url.QueryEscape(name), offset, limit), nil)
	if err != nil {
		return runner.History{}, err
	}
	defer resp.Body.Close()
	var h runner.History
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return runner.History{}, err
	}
	return h, nil
}

// Context returns the context inspector's view of a session.
func (c *Client) Context(ctx context.Context, name string, offset, limit int) (runner.ContextView, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/context?name=%s&offset=%d&limit=%d", url.QueryEscape(name), offset, limit), nil)
	if err != nil {
		return runner.ContextView{}, err
	}
	defer resp.Body.Close()
	var v runner.ContextView
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return runner.ContextView{}, err
	}
	return v, nil
}

// Create names a new session. dir and role may be empty for the defaults.
func (c *Client) Create(ctx context.Context, name, dir, role string) (runner.Meta, error) {
	resp, err := c.do(ctx, http.MethodPost, "/api/create", map[string]any{"name": name, "working_dir": dir, "role": role})
	if err != nil {
		return runner.Meta{}, err
	}
	defer resp.Body.Close()
	var m runner.Meta
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return runner.Meta{}, err
	}
	return m, nil
}

// Models lists configured models and the server's default.
func (c *Client) Models(ctx context.Context) ([]string, string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/models", nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	var out struct {
		Models       []string `json:"models"`
		DefaultModel string   `json:"default_model"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", err
	}
	return out.Models, out.DefaultModel, nil
}

// Roles lists available roles and the server's default.
func (c *Client) Roles(ctx context.Context) ([]string, string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/roles", nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	var out struct {
		Roles       []string `json:"roles"`
		DefaultRole string   `json:"default_role"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", err
	}
	return out.Roles, out.DefaultRole, nil
}

func (c *Client) SetModel(ctx context.Context, name, model string) error {
	return c.post(ctx, "/api/model", map[string]any{"name": name, "model": model})
}
func (c *Client) SetRole(ctx context.Context, name, role string) error {
	return c.post(ctx, "/api/role", map[string]any{"name": name, "role": role})
}
func (c *Client) SetWorkingDir(ctx context.Context, name, dir string) error {
	return c.post(ctx, "/api/working-dir", map[string]any{"name": name, "working_dir": dir})
}

// TrustAll reports a session's auto-approval switch.
func (c *Client) TrustAll(ctx context.Context, name string) (bool, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/trust-all?name="+url.QueryEscape(name), nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var out struct {
		TrustAll bool `json:"trust_all"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.TrustAll, nil
}
func (c *Client) SetTrustAll(ctx context.Context, name string, on bool) error {
	return c.post(ctx, "/api/trust-all", map[string]any{"name": name, "trust_all": on})
}

// Snapshot reports a session's live state. It returns a retained failure state
// for a session whose worker has already stopped, and a 404 when there is no
// live worker and no retained failure.
func (c *Client) Snapshot(ctx context.Context, name string) (runner.Snapshot, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/snapshot?name="+url.QueryEscape(name), nil)
	if err != nil {
		return runner.Snapshot{}, err
	}
	defer resp.Body.Close()
	var s runner.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return runner.Snapshot{}, err
	}
	return s, nil
}

// Send queues an instruction. Reply answers the pending prompt.
func (c *Client) Send(ctx context.Context, name, text string) error {
	return c.post(ctx, "/api/send", map[string]any{"name": name, "text": text})
}
func (c *Client) Steer(ctx context.Context, name, text string) error {
	return c.post(ctx, "/api/steer", map[string]any{"name": name, "text": text})
}
func (c *Client) Cancel(ctx context.Context, name string) error {
	return c.post(ctx, "/api/cancel", map[string]any{"name": name})
}
func (c *Client) Reply(ctx context.Context, name, promptID, text, choice string) error {
	return c.post(ctx, "/api/reply", map[string]any{"name": name, "prompt_id": promptID, "text": text, "choice": choice})
}

func (c *Client) post(ctx context.Context, path string, body any) error {
	resp, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// Event is one SSE frame. Data stays raw so it can be decoded by Type, exactly as
// the browser does.
type Event struct {
	Type     string          `json:"type"`
	Data     json.RawMessage `json:"data,omitempty"`
	Snapshot runner.Snapshot `json:"snapshot"`
}

// Stream subscribes to a session and calls handle for every event, reconnecting
// until ctx is cancelled. A subscription exists only while a worker is live, so
// an idle session is polled until the next instruction starts one.
func (c *Client) Stream(ctx context.Context, name string, handle func(Event)) error {
	return c.stream(ctx, name, nil, handle)
}

// openEvents opens the event stream. A successful call returns a body the caller
// must close; a 404 means no worker is live for the session yet.
func (c *Client) openEvents(ctx context.Context, name string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/events?name="+url.QueryEscape(name), nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// stream reads first (when non-nil) and then reconnects as needed.
func (c *Client) stream(ctx context.Context, name string, first io.ReadCloser, handle func(Event)) error {
	backoff := reconnectMin
	body := first
	for {
		if body == nil {
			opened, err := c.openEvents(ctx, name)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if !retryable(err) {
					return notify(handle, err)
				}
				wait := backoff
				if isNotFound(err) {
					wait = idlePoll // idle is a state, not a failure
				}
				if !pause(ctx, wait) {
					return ctx.Err()
				}
				if !isNotFound(err) {
					backoff = nextBackoff(backoff)
				}
				continue
			}
			body = opened
			backoff = reconnectMin
		}
		err := pump(body, handle) // pump closes the body
		body = nil
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil && !retryable(err) {
			return notify(handle, err)
		}
		if !pause(ctx, backoff) {
			return ctx.Err()
		}
		backoff = nextBackoff(backoff)
	}
}

// pump reads SSE frames until the connection closes. Heartbeats and other
// comment lines are ignored, as is a frame that is not valid JSON: a client must
// not die because one event was unreadable.
func pump(body io.ReadCloser, handle func(Event)) error {
	defer body.Close()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		handle(ev)
	}
	return scanner.Err()
}

// notify surfaces a fatal stream error through the observer, so a detached or
// misconfigured client explains itself instead of going quiet.
func notify(handle func(Event), err error) error {
	if handle != nil {
		if data, marshalErr := json.Marshal(err.Error()); marshalErr == nil {
			handle(Event{Type: "error", Data: data})
		}
	}
	return err
}

func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > reconnectMax {
		return reconnectMax
	}
	return d
}

// Options configures an interactive attach.
type Options struct {
	Client   *Client
	Session  string
	In       io.Reader
	Out      io.Writer
	Observer core.Observer // supplied by the caller so this package stays transport-only
}

// Run attaches to a session, renders its live events through opts.Observer, and
// forwards typed instructions and prompt answers until input ends or ctx is
// cancelled. "/quit" (or EOF) detaches.
func Run(ctx context.Context, opts Options) error {
	if opts.Client == nil || opts.Session == "" {
		return errors.New("conn: missing client or session")
	}
	if opts.Out == nil {
		return errors.New("conn: missing output writer")
	}
	if opts.Observer == nil {
		return errors.New("conn: missing observer")
	}
	a := &attach{client: opts.Client, name: opts.Session, obs: opts.Observer, out: opts.Out}
	// Resolve the session before subscribing: History is also the existence check,
	// and its Meta gives the banner its role, model and working directory.
	h, err := a.client.History(ctx, a.name, 0, 1)
	if err != nil {
		return fmt.Errorf("session %q: %w", a.name, err)
	}
	a.meta = h.Meta
	fmt.Fprintf(a.out, "attached to %s at %s\n  role %s · model %s · cwd %s\n  /help for commands, /quit to detach\n",
		a.meta.Name, a.client.BaseURL, a.meta.Role, a.meta.Model, a.meta.Cwd)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Decide what to show before subscribing. Replay is safe whenever no turn is in
	// flight — no live worker at all, or an idle one with nothing queued or
	// pending. A busy worker may have already stored the turn it is working on, so
	// replaying that turn would print it twice.
	snap, snapErr := a.client.Snapshot(runCtx, a.name)
	switch {
	case snapErr == nil:
		if snap.State == "awaiting" && snap.Queued == 0 && snap.Pending == nil {
			a.replay(ctx, h.Total)
		}
		if snap.Error != "" {
			a.obs.Notice("previous run failed: " + snap.Error)
		}
	case isNotFound(snapErr):
		a.replay(ctx, h.Total)
	default:
		return fmt.Errorf("snapshot: %w", snapErr)
	}
	// A live worker answers the subscription at once; an idle session has no stream
	// to join yet, so the stream polls until the next instruction starts one.
	body, streamErr := a.client.openEvents(runCtx, a.name)
	if streamErr != nil && !isNotFound(streamErr) {
		return fmt.Errorf("events: %w", streamErr)
	}
	go func() { _ = a.client.stream(runCtx, a.name, body, a.handle) }()
	return a.loop(runCtx, opts.In)
}

type attach struct {
	client *Client
	name   string
	obs    core.Observer
	out    io.Writer
	meta   runner.Meta

	mu     sync.Mutex
	prompt *runner.Prompt
}

func (a *attach) loop(ctx context.Context, in io.Reader) error {
	if in == nil {
		return errors.New("conn: missing input reader")
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Local commands win over a pending prompt, so /quit and /trust always work.
		// A line starting with // is sent as-is (one slash stripped), which is how to
		// send a literal instruction that begins with a slash.
		if strings.HasPrefix(line, "/") && !strings.HasPrefix(line, "//") {
			if quit := a.local(ctx, line); quit {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, "//") {
			line = strings.TrimPrefix(line, "/")
		}
		if p := a.takePrompt(); p != nil {
			text, choice := answer(p, line)
			if err := a.client.Reply(ctx, a.name, p.ID, text, choice); err != nil {
				a.fail(err)
			}
			continue
		}
		if err := a.client.Send(ctx, a.name, line); err != nil {
			a.fail(err)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func (a *attach) takePrompt() *runner.Prompt {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.prompt
	a.prompt = nil
	return p
}

func (a *attach) setPrompt(p *runner.Prompt) {
	a.mu.Lock()
	a.prompt = p
	a.mu.Unlock()
	if p != nil {
		fmt.Fprintln(a.out, promptText(p))
	}
}

// promptText renders a pending prompt for a line terminal.
func promptText(p *runner.Prompt) string {
	if p.Kind != "confirm" {
		return "🙋 " + p.Title
	}
	var b strings.Builder
	b.WriteString("🔧 ")
	b.WriteString(p.Title)
	if p.Tool != nil && p.Tool.Detail != "" {
		b.WriteString("\n")
		b.WriteString(p.Tool.Detail)
	}
	if len(p.Untrusted) > 0 {
		b.WriteString("\n⚠ untrusted: ")
		b.WriteString(strings.Join(p.Untrusted, ", "))
	}
	b.WriteString("\nApprove? [y]es/[n]o/[a]lways/[s]ession")
	return b.String()
}

// answer maps a typed line to the prompt's reply. A confirmation is a choice; any
// other prompt (ask) takes the line as its text.
func answer(p *runner.Prompt, line string) (text, choice string) {
	if p.Kind != "confirm" {
		return line, "once"
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "once":
		return "", "once"
	case "s", "session":
		return "", "session"
	case "a", "always":
		return "", "always"
	default:
		return "", "deny"
	}
}

// handle maps one SSE event onto the semantic Observer vocabulary. Events the
// terminal does not render (snapshots, phases, prompts) drive local state only.
func (a *attach) handle(ev Event) {
	switch ev.Type {
	case "snapshot":
		if ev.Snapshot.Pending != nil {
			a.setPrompt(ev.Snapshot.Pending)
		}
	case "prompt":
		var p runner.Prompt
		if json.Unmarshal(ev.Data, &p) == nil {
			a.setPrompt(&p)
		}
	case "prompt_replied", "prompt_cancelled":
		a.mu.Lock()
		a.prompt = nil
		a.mu.Unlock()
	case "reasoning":
		a.text(ev.Data, a.obs.Reasoning)
	case "reason":
		a.text(ev.Data, a.obs.Reason)
	case "tool_output":
		a.text(ev.Data, a.obs.ToolOutput)
	case "done":
		a.text(ev.Data, a.obs.Done)
	case "terminate":
		a.text(ev.Data, a.obs.Terminate)
	case "ask":
		a.text(ev.Data, a.obs.Ask)
	case "user":
		a.text(ev.Data, a.obs.User)
	case "session":
		a.text(ev.Data, a.obs.Session)
	case "output":
		a.text(ev.Data, a.obs.Output)
	case "notice":
		a.text(ev.Data, a.obs.Notice)
	case "error":
		a.text(ev.Data, func(s string) { a.obs.Notice("error: " + s) })
	case "separator":
		if a.obs != nil {
			a.obs.Separator()
		}
	case "compacting":
		if a.obs != nil {
			a.obs.Compacting()
		}
	case "tool_call":
		var c core.ToolCall
		if json.Unmarshal(ev.Data, &c) == nil && a.obs != nil {
			a.obs.ToolCall(c)
		}
	case "tool_result":
		var r core.ToolResult
		if json.Unmarshal(ev.Data, &r) == nil && a.obs != nil {
			a.obs.ToolResult(r)
		}
	case "usage":
		var u core.Usage
		if json.Unmarshal(ev.Data, &u) == nil && a.obs != nil {
			a.obs.Usage(u)
		}
	}
}

func (a *attach) text(data json.RawMessage, emit func(string)) {
	if a.obs == nil || len(data) == 0 {
		return
	}
	var s string
	if json.Unmarshal(data, &s) == nil {
		emit(s)
		return
	}
	// A structured payload is still information; show its JSON rather than
	// dropping the event on the floor.
	emit(string(data))
}
