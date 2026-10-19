package web

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
	"github.com/Carudy/pai/internal/runner"
)

func TestPinnedMarkdownAssets(t *testing.T) {
	h := New(nil, Options{})
	for _, asset := range []struct{ path, version, digest string }{
		{"/vendor/marked.min.js", "18.1.0", "f424dcb508fdf93e0137a970cfce8f3207ea2e3f37eca5f7556a52875683632a"},
		{"/vendor/purify.min.js", "3.4.16", "2c90a9b46d6463f26038a29b686e82bc91de01fdac9d5229e7cfe3b360134ea2"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, asset.path, nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
			t.Fatalf("asset %s: status %d, headers %v", asset.path, w.Code, w.Header())
		}
		if !strings.Contains(w.Body.String(), asset.version) || fmt.Sprintf("%x", sha256.Sum256(w.Body.Bytes())) != asset.digest {
			t.Errorf("asset %s: pinned version/integrity mismatch", asset.path)
		}
		for _, header := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Cache-Control", "Referrer-Policy"} {
			if w.Header().Get(header) == "" {
				t.Errorf("asset %s missing %s", asset.path, header)
			}
		}
	}
	for _, path := range []string{"/vendor/README.md", "/vendor/marked.LICENSE", "/vendor/unknown.js"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 404 {
			t.Errorf("%s: got %d", path, w.Code)
		}
	}
}

type backend struct {
	confirm bool
	// applied records the context surgery the endpoint asked for, keyed by turn,
	// as "action:content". A shared map behind a value receiver is enough because
	// the endpoint test only needs to observe the calls.
	applied map[int]string
}

func (backend) List() ([]runner.Meta, error) {
	return []runner.Meta{{Name: "one", Cwd: "/fixed"}, {Name: "two", Cwd: "/fixed"}}, nil
}
func (backend) History(name string, offset, limit int) (runner.History, error) {
	turns := []core.Turn{{Role: "user", Content: "hello"}, {Role: "assistant", Content: `{"action":"done","payload":"ok"}`}}
	start := min(offset, len(turns))
	end := min(start+limit, len(turns))
	return runner.History{Meta: runner.Meta{Name: name, Cwd: "/fixed"}, Turns: turns[start:end], Total: len(turns)}, nil
}
func (b backend) Prepare(string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), error) {
	return &config.UserConfig{DefaultRole: "devops", DefaultModel: "test:model"}, &questionProvider{confirm: b.confirm}, nil, nil, nil, nil
}
func (backend) Context(name string, offset, limit int) (runner.ContextView, error) {
	if name == "missing" {
		return runner.ContextView{}, runner.ErrNotFound
	}
	cards := []runner.ContextCard{{Seq: 1, Role: "user", Kind: "input", Original: "hello", Sent: "hello", State: "sent"}}
	start := min(offset, len(cards))
	end := min(start+limit, len(cards))
	return runner.ContextView{
		Head:   "SYSTEM HEAD",
		Tail:   "SYSTEM TAIL",
		Model:  "test:model",
		Total:  len(cards),
		Offset: start,
		Cards:  cards[start:end],
		Config: runner.ContextSettings{KeepTurns: 3, MaxTurns: 20},
		// A fixed estimate, so the endpoint test pins the JSON field name.
		EstimatedTokens: 1234,
	}, nil
}
func (b backend) SetOverride(name string, seq int, action, content string) error {
	if name == "missing" {
		return runner.ErrNotFound
	}
	if b.applied != nil {
		b.applied[seq] = action + ":" + content
	}
	return nil
}
func (b backend) DeleteOverride(name string, seq int) error {
	if name == "missing" {
		return runner.ErrNotFound
	}
	if b.applied != nil {
		delete(b.applied, seq)
	}
	return nil
}

var _ runner.ContextBackend = backend{}
var _ runner.ContextSetter = backend{}

type failedPreparationBackend struct{ backend }

func (failedPreparationBackend) Prepare(string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), error) {
	return nil, nil, nil, nil, nil, errors.New("session working directory: directory does not exist")
}

func TestSendPreparationFailureSnapshot(t *testing.T) {
	m := runner.New(context.Background(), failedPreparationBackend{}, 1)
	t.Cleanup(m.Close)
	h := New(m, Options{})
	w := request(h, http.MethodPost, "/api/send", `{"name":"one","text":"hello"}`, nil, "http://pai.test")
	if w.Code != http.StatusAccepted {
		t.Fatalf("send: %d %s", w.Code, w.Body.String())
	}

	// Subscribe only after the failed worker has gone: this is the browser's
	// startup race, where the error event itself can no longer be received.
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, unsubscribe, err := m.Subscribe("one")
		if errors.Is(err, runner.ErrNotFound) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		unsubscribe()
		if time.Now().After(deadline) {
			t.Fatal("failed worker was not removed")
		}
		time.Sleep(time.Millisecond)
	}
	w = request(h, http.MethodGet, "/api/snapshot?name=one", "", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("snapshot: %d %s", w.Code, w.Body.String())
	}
	var s runner.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.State != "stopped" || !strings.Contains(s.Error, "directory does not exist") {
		t.Fatalf("failure was lost: %+v", s)
	}
}

// The context inspector endpoint returns the view the backend supplies, and the
// POST form records surgery on one turn (or clears it, with reset).
func TestContextAPI(t *testing.T) {
	applied := map[int]string{}
	m := runner.New(context.Background(), backend{applied: applied}, 2)
	t.Cleanup(m.Close)
	h := New(m, Options{})
	w := request(h, http.MethodGet, "/api/context?name=one", "", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("context: %d %s", w.Code, w.Body.String())
	}
	var v runner.ContextView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Head == "" || v.Tail == "" || len(v.Cards) != 1 || v.Cards[0].Seq != 1 || v.Config.MaxTurns != 20 || v.Total != 1 || v.Offset != 0 || v.EstimatedTokens != 1234 {
		t.Fatalf("view = %+v", v)
	}
	if w := request(h, http.MethodGet, "/api/context?name=one&offset=1&limit=5", "", nil, ""); w.Code != http.StatusOK {
		t.Errorf("windowed context = %d", w.Code)
	} else {
		var windowed runner.ContextView
		if err := json.Unmarshal(w.Body.Bytes(), &windowed); err != nil {
			t.Fatal(err)
		}
		if windowed.Offset != 1 || len(windowed.Cards) != 0 || windowed.Total != 1 {
			t.Errorf("windowed view = %+v", windowed)
		}
	}
	if w := request(h, http.MethodGet, "/api/context?name=one&limit=0", "", nil, ""); w.Code != http.StatusBadRequest {
		t.Errorf("limit 0 = %d, want 400", w.Code)
	}
	if w := request(h, http.MethodGet, "/api/context?name=missing", "", nil, ""); w.Code != http.StatusNotFound {
		t.Errorf("missing session = %d, want 404", w.Code)
	}

	post := func(body string) int {
		t.Helper()
		return request(h, http.MethodPost, "/api/context", body, nil, "http://pai.test").Code
	}
	if code := post(`{"name":"one","seq":1,"action":"replace","content":"trimmed"}`); code != http.StatusOK {
		t.Fatalf("replace = %d", code)
	}
	if applied[1] != core.OverrideReplace+":trimmed" {
		t.Errorf("replace not recorded: %+v", applied)
	}
	if code := post(`{"name":"one","seq":1,"action":"drop","content":"ignored"}`); code != http.StatusOK {
		t.Fatalf("drop = %d", code)
	}
	if applied[1] != core.OverrideDrop+":" {
		t.Errorf("drop not recorded: %+v", applied)
	}
	if code := post(`{"name":"one","seq":1,"action":"reset"}`); code != http.StatusOK {
		t.Fatalf("reset = %d", code)
	}
	if _, ok := applied[1]; ok {
		t.Errorf("reset did not clear: %+v", applied)
	}
	if code := post(`{"name":"one","seq":1,"action":"bogus"}`); code != http.StatusBadRequest {
		t.Errorf("bad action = %d, want 400", code)
	}
	if code := post(`{"name":"one","seq":0,"action":"drop"}`); code != http.StatusBadRequest {
		t.Errorf("zero seq = %d, want 400", code)
	}
	if code := post(`{"name":"missing","seq":1,"action":"drop"}`); code != http.StatusNotFound {
		t.Errorf("missing session = %d, want 404", code)
	}
	if w := request(h, http.MethodPut, "/api/context", "", nil, "http://pai.test"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT = %d, want 405", w.Code)
	}
}

type questionProvider struct {
	calls   int
	confirm bool
}

func (p *questionProvider) Completion(ctx context.Context, _ provider.CompletionParams) (*provider.ChatCompletion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.calls++
	content := `{"action":"done","payload":"finished","reason":"ok"}`
	if p.calls == 1 {
		content = `{"action":"ask","payload":"Which target?","reason":"clarify"}`
		if p.confirm {
			content = `{"action":"tool","toolname":"execute","payload":"printf web-confirmation","reason":"test approval"}`
		}
	}
	return &provider.ChatCompletion{Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: content}}}}, nil
}
func (*questionProvider) CompletionStream(context.Context, provider.CompletionParams) (<-chan provider.ChatCompletionChunk, <-chan error) {
	panic("unexpected streaming")
}
func setup(t *testing.T, token string) (http.Handler, *runner.Manager) {
	t.Helper()
	m := runner.New(context.Background(), backend{}, 2)
	t.Cleanup(m.Close)
	return New(m, Options{Token: token}), m
}
func request(h http.Handler, method, path, body string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://pai.test"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type creationBackend struct{ backend }

func (creationBackend) Create(name, cwd string) (runner.Meta, error) {
	dir, err := runner.ResolveWorkingDir(cwd)
	return runner.Meta{Name: name, Cwd: dir}, err
}

type modelBackend struct {
	backend
	model   string
	role    string
	renamed string
	deleted string
}

func (*modelBackend) Models() ([]string, string, error) {
	return []string{"test:model", "test:saved"}, "test:model", nil
}
func (b *modelBackend) SetModel(name, model string) error {
	if model != "test:custom" {
		return errors.New("model must use a configured provider: test")
	}
	b.model = model
	return nil
}
func (b *modelBackend) SetRole(name, role string) error {
	if name != "one" {
		return runner.ErrNotFound
	}
	if role != "coder" {
		return errors.New("unknown role")
	}
	b.role = role
	return nil
}
func (b *modelBackend) Rename(name, newName string) error {
	if name != "one" {
		return runner.ErrNotFound
	}
	if newName == "taken" {
		return errors.New("a session with that name already exists")
	}
	b.renamed = newName
	return nil
}
func (b *modelBackend) Delete(name string) error {
	if name != "one" {
		return runner.ErrNotFound
	}
	b.deleted = name
	return nil
}
func (b *modelBackend) History(name string, offset, limit int) (runner.History, error) {
	h, err := b.backend.History(name, offset, limit)
	h.Model = b.model
	return h, err
}

type workingDirBackend struct {
	backend
	mu       sync.Mutex
	dir      string
	starts   int
	cleanups int
}

// bornAt is the session birth the fake backend reports; the runtime pins it into
// the prompt head so a fresh runtime resends the same prefix.
var bornAt = time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)

func (b *workingDirBackend) SetWorkingDir(name, cwd string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if name != "one" {
		return runner.ErrNotFound
	}
	b.dir = cwd
	return nil
}

func (b *workingDirBackend) History(name string, offset, limit int) (runner.History, error) {
	h, err := b.backend.History(name, offset, limit)
	b.mu.Lock()
	h.Cwd = b.dir
	b.mu.Unlock()
	return h, err
}

func (b *workingDirBackend) PrepareWorkspace(name, cwd string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), runner.Meta, error) {
	b.mu.Lock()
	dir := b.dir
	b.starts++
	b.mu.Unlock()
	dir, err := runner.ResolveWorkingDir(dir)
	if err != nil {
		return nil, nil, nil, nil, nil, runner.Meta{}, err
	}
	cfg, p, rec, history, _, err := b.backend.Prepare(name)
	return cfg, p, rec, history, func() { b.mu.Lock(); b.cleanups++; b.mu.Unlock() }, runner.Meta{Cwd: dir, CreatedAt: bornAt}, err
}

func TestWorkingDirAPI(t *testing.T) {
	dir := t.TempDir()
	b := &workingDirBackend{backend: backend{confirm: true}, dir: t.TempDir()}
	m := runner.New(context.Background(), b, 1)
	defer m.Close()
	h := New(m, Options{})
	body := func(name, cwd string) string {
		data, err := json.Marshal(map[string]string{"name": name, "working_dir": cwd})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, body, origin string
		status                     int
	}{
		{"method", "GET", "", "", 405},
		{"missing origin", "POST", body("one", dir), "", 403},
		{"foreign origin", "POST", body("one", dir), "http://evil.test", 403},
		{"blank name", "POST", body(" \t", dir), "http://pai.test", 400},
		{"long name", "POST", body(strings.Repeat("x", 257), dir), "http://pai.test", 400},
		{"missing session", "POST", body("missing", dir), "http://pai.test", 404},
		{"missing directory", "POST", `{"name":"one"}`, "http://pai.test", 400},
		{"blank directory", "POST", body("one", " \t"), "http://pai.test", 400},
		{"nonexistent directory", "POST", body("one", filepath.Join(dir, "missing")), "http://pai.test", 400},
		{"file", "POST", body("one", file), "http://pai.test", 400},
		{"unknown field", "POST", `{"name":"one","working_dir":".","extra":true}`, "http://pai.test", 400},
		{"multiple values", "POST", `{} {}`, "http://pai.test", 400},
		{"malformed JSON", "POST", `{`, "http://pai.test", 400},
		{"wrong type", "POST", `{"name":"one","working_dir":42}`, "http://pai.test", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(h, tc.method, "/api/working-dir", tc.body, nil, tc.origin)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
	r := httptest.NewRequest("POST", "http://pai.test/api/working-dir", strings.NewReader(body("one", dir)))
	r.Header.Set("Origin", "http://pai.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatalf("content type: %d %s", w.Code, w.Body.String())
	}
	if err := m.Send("one", "task"); err != nil {
		t.Fatal(err)
	}
	pending := wait(t, m, func(s runner.Snapshot) bool { return s.Pending != nil })
	if w := request(h, "POST", "/api/working-dir", body("one", dir), nil, "http://pai.test"); w.Code != 409 {
		t.Fatalf("busy: %d %s", w.Code, w.Body.String())
	}
	if err := m.Reply("one", pending.Pending.ID, "", core.TrustDeny); err != nil {
		t.Fatal(err)
	}
	wait(t, m, func(s runner.Snapshot) bool { return s.State == "awaiting" })
	before, err := m.History("one", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	w = request(h, "POST", "/api/working-dir", body("one", dir+string(os.PathSeparator)+"."), nil, "http://pai.test")
	var result map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || !result["ok"] {
		t.Fatalf("switch: %d %s", w.Code, w.Body.String())
	}
	if w := request(h, "GET", "/api/snapshot?name=one", "", nil, ""); w.Code != 404 {
		t.Fatalf("retirement: %d %s", w.Code, w.Body.String())
	}
	w = request(h, "GET", "/api/history?name=one", "", nil, "")
	var after runner.History
	if err := json.Unmarshal(w.Body.Bytes(), &after); err != nil || w.Code != 200 || after.Cwd != dir || after.Total != before.Total || !reflect.DeepEqual(after.Turns, before.Turns) {
		t.Fatalf("persisted workspace/history: %d %s", w.Code, w.Body.String())
	}
	b.mu.Lock()
	starts, cleanups := b.starts, b.cleanups
	b.mu.Unlock()
	if starts != 1 || cleanups != 1 {
		t.Fatalf("switch did not retire lazily: starts=%d cleanups=%d", starts, cleanups)
	}
	if w := request(h, "POST", "/api/send", `{"name":"one","text":"again"}`, nil, "http://pai.test"); w.Code != 202 {
		t.Fatalf("send after switch: %d %s", w.Code, w.Body.String())
	}
	wait(t, m, func(s runner.Snapshot) bool { return s.Pending != nil })
	b.mu.Lock()
	starts = b.starts
	b.mu.Unlock()
	if starts != 2 {
		t.Fatal("next send did not reconstruct runtime")
	}
}

func TestWorkingDirAPIAuthentication(t *testing.T) {
	b := &workingDirBackend{dir: t.TempDir()}
	m := runner.New(context.Background(), b, 1)
	defer m.Close()
	h := New(m, Options{Token: "secret"})
	data, err := json.Marshal(map[string]string{"name": "one", "working_dir": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if w := request(h, "POST", "/api/working-dir", body, nil, "http://pai.test"); w.Code != 401 {
		t.Fatalf("unauthenticated: %d %s", w.Code, w.Body.String())
	}
	login := request(h, "POST", "/api/login", `{"token":"secret"}`, nil, "http://pai.test")
	cookies := login.Result().Cookies()
	if login.Code != 200 || len(cookies) != 1 {
		t.Fatalf("login: %d %s", login.Code, login.Body.String())
	}
	if w := request(h, "POST", "/api/working-dir", body, cookies[0], "http://evil.test"); w.Code != 403 {
		t.Fatalf("authenticated foreign origin: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "http://pai.test/api/working-dir", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://pai.test")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.AddCookie(cookies[0])
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-site: %d", w.Code)
	}
	if w := request(h, "POST", "/api/working-dir", body, cookies[0], "http://pai.test"); w.Code != 200 {
		t.Fatalf("authenticated switch: %d %s", w.Code, w.Body.String())
	}
}

func TestModelsAPI(t *testing.T) {
	b := &modelBackend{backend: backend{confirm: true}}
	m := runner.New(context.Background(), b, 1)
	defer m.Close()
	h := New(m, Options{})
	w := request(h, "GET", "/api/models", "", nil, "")
	var discovery struct {
		Models  []string `json:"models"`
		Default string   `json:"default_model"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &discovery); err != nil || w.Code != 200 || len(discovery.Models) != 2 || discovery.Default != "test:model" {
		t.Fatalf("models: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		method, path, body, origin string
		status                     int
	}{
		{"POST", "/api/models", `{}`, "http://pai.test", 405},
		{"GET", "/api/model", "", "", 405},
		{"POST", "/api/model", `{"name":"one","model":"test:custom"}`, "", 403},
		{"POST", "/api/model", `{"name":"one","model":"test:custom"}`, "http://evil.test", 403},
		{"POST", "/api/model", `{"name":"","model":"test:custom"}`, "http://pai.test", 400},
		{"POST", "/api/model", `{"name":"one","model":"invalid"}`, "http://pai.test", 400},
		{"POST", "/api/model", `{"name":"one","model":"test:custom","api_key":"secret"}`, "http://pai.test", 400},
		{"POST", "/api/model", `{} {}`, "http://pai.test", 400},
	} {
		if w := request(h, tc.method, tc.path, tc.body, nil, tc.origin); w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/models", "/api/model"} {
		method, body := "GET", ""
		if path == "/api/model" {
			method, body = "POST", `{"name":"one","model":"test:custom"}`
		}
		if w := request(New(m, Options{Token: "secret"}), method, path, body, nil, "http://pai.test"); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if err := m.Send("one", "task"); err != nil {
		t.Fatal(err)
	}
	pending := wait(t, m, func(s runner.Snapshot) bool { return s.Pending != nil })
	if w := request(h, "POST", "/api/model", `{"name":"one","model":"test:custom"}`, nil, "http://pai.test"); w.Code != 409 || b.model != "" {
		t.Fatalf("busy: %d %s", w.Code, w.Body.String())
	}
	if err := m.Reply("one", pending.Pending.ID, "target", core.TrustDeny); err != nil {
		t.Fatal(err)
	}
	wait(t, m, func(s runner.Snapshot) bool { return s.State == "awaiting" })
	if w := request(h, "POST", "/api/model", `{"name":"one","model":"test:custom"}`, nil, "http://pai.test"); w.Code != 200 {
		t.Fatalf("switch: %d %s", w.Code, w.Body.String())
	}
	if w := request(h, "GET", "/api/snapshot?name=one", "", nil, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	w = request(h, "GET", "/api/history?name=one", "", nil, "")
	var history runner.History
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil || history.Model != "test:custom" || history.Total != 2 {
		t.Fatalf("persisted: %s", w.Body.String())
	}
}

// The composer's sudo switch is the same session grant a confirmation makes, for
// one name at a time.
func TestCommandTrustAPI(t *testing.T) {
	h, m := setup(t, "")
	if w := request(h, "POST", "/api/trust-cmd", `{"name":"one","cmd":"sudo","on":true}`, nil, "http://pai.test"); w.Code != 200 || !strings.Contains(w.Body.String(), `"trusted_cmds":["sudo"]`) {
		t.Fatalf("grant: %d %s", w.Code, w.Body.String())
	}
	if got := m.CommandTrust("one"); len(got) != 1 || got[0] != "sudo" {
		t.Fatalf("CommandTrust = %v, want [sudo]", got)
	}
	if w := request(h, "POST", "/api/trust-cmd", `{"name":"one","cmd":"sudo rm","on":true}`, nil, "http://pai.test"); w.Code != 400 {
		t.Fatalf("a command line is not a name: %d %s", w.Code, w.Body.String())
	}
	if w := request(h, "POST", "/api/trust-cmd", `{"name":"one","cmd":"sudo","on":false}`, nil, "http://pai.test"); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	if got := m.CommandTrust("one"); len(got) != 0 {
		t.Fatalf("revoked still granted: %v", got)
	}
	if w := request(h, "GET", "/api/trust-cmd?name=one", "", nil, ""); w.Code != 405 {
		t.Fatalf("GET: %d", w.Code)
	}
}

func TestTrustAllAPI(t *testing.T) {
	h, m := setup(t, "")
	if w := request(h, "GET", "/api/trust-all?name=one", "", nil, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"trust_all":false`) {
		t.Fatalf("initial read: %d %s", w.Code, w.Body.String())
	}
	if w := request(h, "POST", "/api/trust-all", `{"name":"one","trust_all":true}`, nil, "http://pai.test"); w.Code != 200 {
		t.Fatalf("enable: %d %s", w.Code, w.Body.String())
	}
	if !m.TrustAll("one") {
		t.Fatal("manager did not record trust-all")
	}
	if w := request(h, "GET", "/api/trust-all?name=one", "", nil, ""); !strings.Contains(w.Body.String(), `"trust_all":true`) {
		t.Fatalf("read back: %s", w.Body.String())
	}
	if w := request(h, "POST", "/api/trust-all", `{"name":"one","trust_all":false}`, nil, "http://pai.test"); w.Code != 200 {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if m.TrustAll("one") {
		t.Fatal("disable ignored")
	}

	for _, tc := range []struct {
		name, method, body, origin string
		status                     int
	}{
		{"method", "PUT", "", "http://pai.test", 405},
		{"missing origin", "POST", `{"name":"one","trust_all":true}`, "", 403},
		{"foreign origin", "POST", `{"name":"one","trust_all":true}`, "http://evil.test", 403},
		{"blank name", "POST", `{"name":"  ","trust_all":true}`, "http://pai.test", 400},
		{"long name", "POST", `{"name":"` + strings.Repeat("x", 257) + `","trust_all":true}`, "http://pai.test", 400},
		{"missing session", "POST", `{"name":"ghost","trust_all":true}`, "http://pai.test", 404},
		{"unknown field", "POST", `{"name":"one","trust_all":true,"extra":1}`, "http://pai.test", 400},
		{"wrong type", "POST", `{"name":"one","trust_all":"yes"}`, "http://pai.test", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := request(h, tc.method, "/api/trust-all", tc.body, nil, tc.origin); w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

// The composer's trust switch and auto-reply button are load-bearing, so guard
// them in the shipped markup where a Go test can see them (app_test.js only runs
// when Node is present).
func TestComposerControlsPresent(t *testing.T) {
	data, err := assets.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	for _, want := range []string{`id="trust-all"`, `id="trust-sudo"`, `id="apply-plan"`, `class="switch"`, `class="composer-trust"`, `id="state-icon"`} {
		if !strings.Contains(html, want) {
			t.Errorf("composer markup missing %s", want)
		}
	}
}

// The new-session role control is a radio grid and the timeline carries both jump
// buttons; guard the load-bearing markup where a Go test can see it (app_test.js
// only runs when Node is present).
func TestSessionControlsPresent(t *testing.T) {
	data, err := assets.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	for _, want := range []string{`id="to-bottom"`, `id="role-fieldset"`, `class="role-options"`, `role="radiogroup"`, `<legend>Role</legend>`} {
		if !strings.Contains(html, want) {
			t.Errorf("session markup missing %s", want)
		}
	}
}

// The workspace shell ships hidden and is revealed only after the first
// authenticated request, so a signed-out load cannot flash it before the login
// form appears.
func TestWorkspaceShellStartsHidden(t *testing.T) {
	data, err := assets.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `<div id="app" hidden>`) {
		t.Error("index.html must ship the workspace shell hidden")
	}
}

func TestDeleteAPI(t *testing.T) {
	b := &modelBackend{}
	m := runner.New(context.Background(), b, 1)
	defer m.Close()
	h := New(m, Options{})
	if w := request(h, "POST", "/api/delete", `{"name":"one"}`, nil, "http://pai.test"); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if b.deleted != "one" {
		t.Fatalf("backend deleted = %q, want one", b.deleted)
	}
	for _, tc := range []struct {
		method, body, origin string
		status               int
	}{
		{"GET", "", "", 405},
		{"POST", `{"name":""}`, "http://pai.test", 400},
		{"POST", `{"name":"one"}`, "", 403},
		{"POST", `{"name":"one","extra":1}`, "http://pai.test", 400},
		{"POST", `{"name":"ghost"}`, "http://pai.test", 404},
	} {
		if w := request(h, tc.method, "/api/delete", tc.body, nil, tc.origin); w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.body, w.Code, w.Body.String())
		}
	}
}

func TestRoleAndRenameAPI(t *testing.T) {
	b := &modelBackend{}
	m := runner.New(context.Background(), b, 1)
	defer m.Close()
	h := New(m, Options{})
	if w := request(h, "POST", "/api/role", `{"name":"one","role":"coder"}`, nil, "http://pai.test"); w.Code != 202 {
		t.Fatalf("role: %d %s", w.Code, w.Body.String())
	}
	if b.role != "coder" {
		t.Fatalf("backend role = %q, want coder", b.role)
	}
	if w := request(h, "POST", "/api/rename", `{"name":"one","new_name":"two"}`, nil, "http://pai.test"); w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	if b.renamed != "two" {
		t.Fatalf("backend renamed = %q, want two", b.renamed)
	}
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/api/role", `{"name":"one","role":""}`, 400},
		{"/api/role", `{"name":"one","role":"ghost"}`, 400},
		{"/api/role", `{"name":"ghost","role":"coder"}`, 404},
		{"/api/rename", `{"name":"one","new_name":""}`, 400},
		{"/api/rename", `{"name":"one","new_name":"taken"}`, 400},
		{"/api/rename", `{"name":"ghost","new_name":"two"}`, 404},
	} {
		if w := request(h, "POST", tc.path, tc.body, nil, "http://pai.test"); w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.path, tc.body, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/role", "/api/rename"} {
		if w := request(h, "GET", path, "", nil, ""); w.Code != 405 {
			t.Fatalf("GET %s = %d, want 405", path, w.Code)
		}
	}
}

type rewindBackend struct {
	backend
	keep int
}

func (b *rewindBackend) Rewind(name string, keep int, backup *bool) error {
	if name != "one" {
		return runner.ErrNotFound
	}
	b.keep = keep
	return nil
}

func TestRewindAPI(t *testing.T) {
	b := &rewindBackend{}
	m := runner.New(context.Background(), b, 1)
	defer m.Close()
	h := New(m, Options{})
	if w := request(h, "POST", "/api/rewind", `{"name":"one","keep":2}`, nil, "http://pai.test"); w.Code != 202 {
		t.Fatalf("rewind: %d %s", w.Code, w.Body.String())
	}
	if b.keep != 2 {
		t.Fatalf("backend keep = %d, want 2", b.keep)
	}
	if w := request(h, "GET", "/api/rewind", "", nil, ""); w.Code != 405 {
		t.Fatalf("method = %d", w.Code)
	}
	for _, tc := range []struct {
		body   string
		origin string
		status int
	}{
		{`{"name":"one","keep":-1}`, "http://pai.test", 400},
		{`{"name":"","keep":0}`, "http://pai.test", 400},
		{`{"name":"one","keep":0}`, "", 403},
		{`{"name":"ghost","keep":0}`, "http://pai.test", 404},
		{`{"name":"one","keep":0,"extra":1}`, "http://pai.test", 400},
	} {
		if w := request(h, "POST", "/api/rewind", tc.body, nil, tc.origin); w.Code != tc.status {
			t.Fatalf("rewind %s: %d %s", tc.body, w.Code, w.Body.String())
		}
	}
}

type roleBackend struct{ creationBackend }

func (roleBackend) Roles() ([]string, string, error) {
	return []string{"custom", "default"}, "default", nil
}
func (b roleBackend) CreateWithRole(name, cwd, role string) (runner.Meta, error) {
	if role == "" {
		role = "default"
	}
	if role != "custom" && role != "default" {
		return runner.Meta{}, errors.New("unknown role")
	}
	meta, err := b.Create(name, cwd)
	meta.Role = role
	return meta, err
}

func TestRolesAPI(t *testing.T) {
	m := runner.New(context.Background(), roleBackend{}, 1)
	defer m.Close()
	h := New(m, Options{})
	w := request(h, "GET", "/api/roles", "", nil, "")
	var body struct {
		Roles   []string `json:"roles"`
		Default string   `json:"default_role"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || len(body.Roles) != 2 || body.Default != "default" {
		t.Fatalf("roles: %d %s", w.Code, w.Body.String())
	}
	if w := request(New(m, Options{Token: "secret"}), "GET", "/api/roles", "", nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(h, "POST", "/api/roles", `{}`, nil, "http://pai.test"); w.Code != 405 {
		t.Fatal(w.Code)
	}
	for _, role := range []string{"", "custom", "invalid"} {
		body, _ := json.Marshal(map[string]string{"name": "selected", "working_dir": t.TempDir(), "role": role})
		w := request(h, "POST", "/api/create", string(body), nil, "http://pai.test")
		if role == "invalid" {
			if w.Code != 400 {
				t.Fatal(w.Code)
			}
			continue
		}
		var meta runner.Meta
		if role == "" {
			role = "default"
		}
		if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil || w.Code != 201 || meta.Role != role {
			t.Fatalf("create role: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestCreateAPI(t *testing.T) {
	m := runner.New(context.Background(), creationBackend{}, 1)
	defer m.Close()
	h := New(m, Options{})
	dir := t.TempDir()
	body, _ := json.Marshal(map[string]string{"name": "created", "working_dir": dir})
	w := request(h, "POST", "/api/create", string(body), nil, "http://pai.test")
	var meta runner.Meta
	if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil || w.Code != 201 || meta.Name != "created" || meta.Cwd != dir {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if _, err := m.Snapshot("created"); !errors.Is(err, runner.ErrNotFound) {
		t.Fatal("creation started worker")
	}
	for _, tc := range []struct {
		method, body, origin string
		code                 int
	}{
		{"GET", "", "", 405},
		{"POST", string(body), "https://evil.test", 403},
		{"POST", `{}`, "http://pai.test", 400},
		{"POST", `{"name":"bad","working_dir":"/nonexistent-pai-workspace"}`, "http://pai.test", 400},
		{"POST", `{"name":"bad","extra":true}`, "http://pai.test", 400},
	} {
		w = request(h, tc.method, "/api/create", tc.body, nil, tc.origin)
		if w.Code != tc.code {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	auth := New(m, Options{Token: "secret"})
	if w := request(auth, "POST", "/api/create", string(body), nil, "http://pai.test"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	legacy, _ := setup(t, "")
	if w := request(legacy, "POST", "/api/send", `{"name":"one","text":"task","working_dir":"`+dir+`"}`, nil, "http://pai.test"); w.Code != 400 || !strings.Contains(w.Body.String(), "does not support working directories") {
		t.Fatalf("send: %d %s", w.Code, w.Body.String())
	}
}

// Assets are gzip-encoded when the client asks, and decompress to the embedded
// bytes unchanged.
func TestAssetsGzip(t *testing.T) {
	h, _ := setup(t, "")
	r := httptest.NewRequest(http.MethodGet, "/style.css", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status %d, encoding %q", w.Code, w.Header().Get("Content-Encoding"))
	}
	if w.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("missing Vary: Accept-Encoding")
	}
	gz, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	got, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want, _ := assets.ReadFile("style.css")
	if string(got) != string(want) {
		t.Error("decompressed body differs from the embedded asset")
	}
}

// The sessions list carries the server's launch directory so the UI can prefill
// a new session's working directory.
func TestSessionsExposeDefaultCwd(t *testing.T) {
	m := runner.New(context.Background(), backend{}, 1)
	t.Cleanup(m.Close)
	h := New(m, Options{ServerCwd: "/srv/work"})

	w := request(h, "GET", "/api/sessions", "", nil, "")
	if w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	var body struct {
		DefaultCwd string `json:"default_cwd"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.DefaultCwd != "/srv/work" {
		t.Errorf("default_cwd = %q, want /srv/work", body.DefaultCwd)
	}
}

func TestPageLoadsSessionUIAssets(t *testing.T) {
	h, _ := setup(t, "")
	w := request(h, "GET", "/", "", nil, "")
	if w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	for _, markup := range []string{`<script src="/app.js" defer></script>`, `id="sessions"`} {
		if !strings.Contains(w.Body.String(), markup) {
			t.Fatalf("page missing required markup %q", markup)
		}
	}
	w = request(h, "GET", "/api/sessions", "", nil, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"one"`) {
		t.Fatalf("saved sessions unavailable: %d %s", w.Code, w.Body.String())
	}
}

func TestHTMLJavaScriptIDContract(t *testing.T) {
	h, _ := setup(t, "")
	html := request(h, "GET", "/", "", nil, "").Body.String()
	js := request(h, "GET", "/app.js", "", nil, "").Body.String()
	ids := make(map[string]bool)
	for _, match := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		if ids[match[1]] {
			t.Errorf("duplicate HTML id %q", match[1])
		}
		ids[match[1]] = true
	}
	refs := regexp.MustCompile(`\$\(\s*['"]([^'"]+)['"]\s*\)`).FindAllStringSubmatch(js, -1)
	if len(refs) == 0 {
		t.Fatal("no literal JavaScript id references found")
	}
	for _, match := range refs {
		if !ids[match[1]] {
			t.Errorf("JavaScript references missing HTML id %q", match[1])
		}
	}
	if !strings.Contains(html, "Best partner, PAI!") {
		t.Error("missing welcome")
	}
}

// `element.children` is an HTMLCollection in a browser (no filter/find/map), even
// though the dependency-free test double uses a plain array. Calling an Array
// method on it throws at load and wedges the UI, so guard against it in source.
func TestAppDoesNotCallArrayMethodsOnChildren(t *testing.T) {
	h, _ := setup(t, "")
	js := request(h, "GET", "/app.js", "", nil, "").Body.String()
	bad := regexp.MustCompile(`\.children(?:\s*\|\|\s*\[\]\))?\s*\.(filter|find|map|some|every|forEach)\s*\(`)
	if m := bad.FindString(js); m != "" {
		t.Fatalf("app.js calls an array method on element.children: %q", m)
	}
}

func TestPageLayoutNesting(t *testing.T) {
	h, _ := setup(t, "")
	page := request(h, "GET", "/", "", nil, "").Body.String()
	if !strings.Contains(page, `<div class="composer-dock">`) {
		t.Fatal("missing composer container")
	}
	// Browser recovery of malformed container tags can move the activity panel
	// outside the viewport grid, even when all required IDs are present.
	var stack []string
	var containers []string
	parents := make(map[string]string)
	for _, tag := range regexp.MustCompile(`<(/?)([a-z][a-z0-9-]*)\b[^>]*>`).FindAllStringSubmatch(page, -1) {
		name := tag[2]
		if strings.Contains(tag[0][1:], "<") {
			t.Fatalf("malformed tag: %s", tag[0])
		}
		switch name {
		case "meta", "link", "input", "br", "hr", "img":
			continue
		}
		if tag[1] == "" {
			container := name
			if match := regexp.MustCompile(`id="([^"]+)"|class="([^"]+)"`).FindStringSubmatch(tag[0]); match != nil {
				container = match[1] + match[2]
			}
			if len(containers) > 0 {
				parents[container] = containers[len(containers)-1]
			}
			containers = append(containers, container)
			stack = append(stack, name)
			continue
		}
		if len(stack) == 0 || stack[len(stack)-1] != name {
			t.Fatalf("unbalanced layout tag %s (stack %v)", tag[0], stack)
		}
		stack = stack[:len(stack)-1]
		containers = containers[:len(containers)-1]
	}
	if len(stack) != 0 {
		t.Fatalf("unclosed layout containers: %v", stack)
	}
	for child, parent := range map[string]string{
		"conversation-wrapper": "main", "conversation": "conversation-wrapper",
		"pending": "conversation-wrapper", "history": "conversation",
		"composer-dock": "main", "composer": "composer-dock", "activity": "app",
		"session-config": "header", "config-body": "session-config", "config-row": "config-body", "model-form": "config-row", "role-form": "config-row",
		"working-dir-form": "config-row", "reasoning-form": "config-row",
		"apply-model": "model-form", "reasoning-preview": "conversation-wrapper", "prev-input": "conversation-wrapper",
		"to-bottom": "conversation-wrapper", "session-role": "role-fieldset",
		"session-title-row": "header", "token-usage": "session-config", "state": "status-row",
		"toggle-config": "session-config", "context-usage": "header", "context-detail": "context-usage", "compress-now": "context-usage",
	} {
		if parents[child] != parent {
			t.Errorf("%s parent = %q, want %q", child, parents[child], parent)
		}
	}
	if !strings.Contains(page, `aria-labelledby="pending-title"`) {
		t.Error("pending overlay missing accessible title reference")
	}
}

// An unknown reply choice is rejected before it reaches the manager.
func TestReplyRejectsUnknownChoice(t *testing.T) {
	h, _ := setup(t, "")
	w := request(h, "POST", "/api/reply", `{"name":"one","prompt_id":"1","choice":"bogus"}`, nil, "http://pai.test")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
}

func TestAuthenticationAndSecurity(t *testing.T) {
	h, _ := setup(t, "secret")
	if w := request(h, "GET", "/api/sessions", "", nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, origin := range []string{"", "null", "http://evil.test", "https://pai.test", "http://pai.test/path", "http://user@pai.test"} {
		if w := request(h, "POST", "/api/login", `{"token":"secret"}`, nil, origin); w.Code != 403 {
			t.Fatalf("origin %q: %d", origin, w.Code)
		}
	}
	if w := request(h, "POST", "/api/login", `{"token":"wrong"}`, nil, "http://pai.test"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request(h, "POST", "/api/login", `{"token":"secret"}`, nil, "http://pai.test")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing cookie")
	}
	cookie := cookies[0]
	if cookie.Value == "secret" || len(cookie.Value) != 64 || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure {
		t.Fatalf("bad cookie: %+v", cookie)
	}
	if w = request(h, "GET", "/api/sessions", "", cookie, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	for _, header := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Cache-Control"} {
		if w.Header().Get(header) == "" {
			t.Fatal(header)
		}
	}
	if w = request(h, "GET", "/api/sessions?token=secret", "", cookie, ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("POST", "http://pai.test/api/login", strings.NewReader(`{"token":"secret"}`))
	r.Header.Set("Origin", "http://pai.test")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("POST", "https://pai.test/api/login", strings.NewReader(`{"token":"secret"}`))
	r.TLS = &tls.ConnectionState{}
	r.Header.Set("Origin", "https://pai.test")
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !w.Result().Cookies()[0].Secure {
		t.Fatal("TLS cookie not secure")
	}
	// Mutations must pass the origin gate even when authentication is disabled.
	open, _ := setup(t, "")
	if w = request(open, "POST", "/api/send", `{"name":"one","text":"hello"}`, nil, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestPendingPromptUI(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable; run node internal/web/app_test.js to check UI regressions")
	}
	output, err := exec.Command(node, "app_test.js").CombinedOutput()
	if err != nil {
		t.Fatalf("pending prompt UI: %v\n%s", err, output)
	}
}

func TestPublicOrigin(t *testing.T) {
	_, m := setup(t, "secret")
	for _, public := range []string{"https://public.example:8443", "http://public.example:8080"} {
		h := New(m, Options{Token: "secret", PublicOrigin: public})
		w := request(h, "POST", "/api/login", `{"token":"secret"}`, nil, public)
		if w.Code != 200 {
			t.Fatalf("public origin login: %d %s", w.Code, w.Body.String())
		}
		cookie := w.Result().Cookies()[0]
		if cookie.Secure != strings.HasPrefix(public, "https://") {
			t.Fatalf("public cookie: %+v", cookie)
		}
		for _, origin := range []string{"http://pai.test", "https://evil.example", public + "/", public + "?", public + "#", ""} {
			w = request(h, "POST", "/api/cancel", `{"name":"missing"}`, cookie, origin)
			if w.Code != 403 {
				t.Fatalf("public %s, origin %q: %d", public, origin, w.Code)
			}
		}
		w = request(h, "POST", "/api/cancel", `{"name":"missing"}`, cookie, public)
		if w.Code != 404 {
			t.Fatalf("public origin mutation: %d", w.Code)
		}
	}
	// Forwarded headers cannot supply either the origin or secure-cookie policy.
	for _, public := range []string{"", "https://public.example"} {
		h := New(m, Options{Token: "secret", PublicOrigin: public})
		r := httptest.NewRequest("POST", "http://pai.test/api/login", strings.NewReader(`{"token":"secret"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://evil.example")
		r.Header.Set("Forwarded", `host=evil.example;proto=https`)
		r.Header.Set("X-Forwarded-Host", "evil.example")
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("trusted forwarded origin")
		}
		if public == "" {
			r.Header.Set("Origin", "http://pai.test")
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 || w.Result().Cookies()[0].Secure {
				t.Fatal("trusted forwarded TLS")
			}
		}
	}
	for _, public := range []string{"https://public.example/", "ftp://public.example", "https://user@public.example", "https://public.example?", "https://public.example#", "not-an-origin"} {
		h := New(m, Options{PublicOrigin: public})
		w := request(h, "POST", "/api/login", `{"token":""}`, nil, "http://pai.test")
		if w.Code != 403 {
			t.Fatalf("invalid public origin fell back: %s", public)
		}
	}
}

// AllowOrigins accepts extra browser origins for mutations, so one server can
// answer on a second address (a LAN IP, say) while PublicOrigin stays canonical.
// It widens nothing else: another origin still has to authenticate there, and an
// unlisted one is still rejected.
func TestAllowedOrigins(t *testing.T) {
	_, m := setup(t, "secret")
	const public = "https://public.example:8443"
	const extra = "http://192.168.1.5:9384"
	h := New(m, Options{Token: "secret", PublicOrigin: public, AllowOrigins: []string{extra}})

	for _, tc := range []struct {
		origin string
		secure bool
	}{
		{public, true},
		// A plain-HTTP origin must not be handed a Secure cookie: the browser would
		// drop it, so login would succeed and the next request would 401.
		{extra, false},
	} {
		w := request(h, "POST", "/api/login", `{"token":"secret"}`, nil, tc.origin)
		if w.Code != 200 {
			t.Fatalf("origin %q login: %d %s", tc.origin, w.Code, w.Body.String())
		}
		cookie := w.Result().Cookies()[0]
		if cookie.Secure != tc.secure {
			t.Fatalf("origin %q cookie Secure = %v, want %v", tc.origin, cookie.Secure, tc.secure)
		}
		if w = request(h, "POST", "/api/cancel", `{"name":"missing"}`, cookie, tc.origin); w.Code != 404 {
			t.Fatalf("origin %q mutation: %d", tc.origin, w.Code)
		}
	}
	for _, origin := range []string{"http://192.168.1.6:9384", "http://evil.example", ""} {
		if w := request(h, "POST", "/api/cancel", `{"name":"missing"}`, nil, origin); w.Code != 403 {
			t.Fatalf("unlisted origin %q: %d", origin, w.Code)
		}
	}
	// The extra origin is matched strictly: a different scheme or port is not it.
	for _, origin := range []string{"https://192.168.1.5:9384", "http://192.168.1.5:9385", "http://192.168.1.5"} {
		if w := request(h, "POST", "/api/cancel", `{"name":"missing"}`, nil, origin); w.Code != 403 {
			t.Fatalf("near-miss origin %q: %d", origin, w.Code)
		}
	}
}

// A session URL serves the single-page shell, so a link or a reload opens that
// conversation; anything else under the prefix is still a 404, and nothing is
// ever read from disk by that name.
func TestSessionURLServesTheShell(t *testing.T) {
	h, _ := setup(t, "")
	for _, path := range []string{"/", "/session/one", "/session/20260101-120000-abcd"} {
		w := request(h, "GET", path, "", nil, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>PAI workspace</title>") {
			t.Fatalf("GET %s = %d %q", path, w.Code, w.Body.String()[:min(40, len(w.Body.String()))])
		}
	}
	for _, path := range []string{"/session/", "/session/a/b", "/session", "/sessions/one", "/session/one/"} {
		if w := request(h, "GET", path, "", nil, ""); w.Code != 404 {
			t.Fatalf("GET %s = %d, want 404", path, w.Code)
		}
	}
	if w := request(h, "POST", "/session/one", "", nil, ""); w.Code != 405 {
		t.Fatalf("POST /session/one = %d, want 405", w.Code)
	}
}

func TestConfirmationSnapshotRetainsTool(t *testing.T) {
	m := runner.New(context.Background(), backend{confirm: true}, 1)
	t.Cleanup(m.Close)
	h := New(m, Options{})
	w := request(h, "POST", "/api/send", `{"name":"one","text":"request confirmation"}`, nil, "http://pai.test")
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	s := wait(t, m, func(s runner.Snapshot) bool { return s.Pending != nil })
	if s.Pending.Kind != "confirm" || s.Pending.Tool == nil {
		t.Fatalf("missing retained tool: %+v", s.Pending)
	}
	for i := 0; i < 2; i++ {
		w = request(h, "GET", "/api/snapshot?name=one", "", nil, "")
		var got runner.Snapshot
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Pending == nil || got.Pending.ID != s.Pending.ID || got.Pending.Tool == nil || got.Pending.Tool.Name != "execute" || got.Pending.Tool.Detail != "printf web-confirmation" {
			t.Fatalf("snapshot lost approval context: %+v", got.Pending)
		}
	}
	server := httptest.NewServer(h)
	defer server.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	for i := 0; i < 2; i++ {
		response, err := client.Get(server.URL + "/api/events?name=one")
		if err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(response.Body).ReadString('\n')
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		var event runner.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Snapshot.Pending == nil || event.Snapshot.Pending.Tool == nil || !reflect.DeepEqual(*event.Snapshot.Pending.Tool, *s.Pending.Tool) {
			t.Fatal("SSE reconnect lost tool")
		}
	}
}

func TestLimitsPaginationAndMethods(t *testing.T) {
	h, _ := setup(t, "")
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/api/sessions?limit=1&offset=1", "", 200},
		{"GET", "/api/history?name=one&offset=1&limit=1", "", 200},
		{"GET", "/api/history?limit=201", "", 400},
		{"GET", "/api/history?offset=-1", "", 400},
		{"GET", "/api/send", "", 405},
		{"POST", "/api/send", `{"name":"one","text":"x","unexpected":1}`, 400},
		{"POST", "/api/send", `{"name":"one","text":"x"} {}`, 400},
		{"POST", "/api/send", `{"name":"one","text":"` + strings.Repeat("x", requestLimit) + `"}`, 413},
		{"POST", "/api/send", `{"name":"","text":"x"}`, 400},
		{"POST", "/api/send", `{"name":"one","text":"/new"}`, 400},
		{"POST", "/api/cancel", `{"name":"missing"}`, 404},
		{"GET", "/api/snapshot?name=missing", "", 404},
	} {
		w := request(h, tc.method, tc.path, tc.body, nil, "http://pai.test")
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	w := request(h, "GET", "/api/sessions?offset=1&limit=1", "", nil, "")
	var page struct {
		Sessions []runner.Meta
		Total    int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Sessions) != 1 || page.Sessions[0].Name != "two" {
		t.Fatal(page)
	}
	r := httptest.NewRequest("POST", "http://pai.test/api/login", strings.NewReader(`{}`))
	r.Header.Set("Origin", "http://pai.test")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatal(w.Code)
	}
}
func wait(t *testing.T, m *runner.Manager, match func(runner.Snapshot) bool) runner.Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, err := m.Snapshot("one")
		if err == nil && match(s) {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("runtime did not reach expected state")
	return runner.Snapshot{}
}
func TestCancelPendingAndSSECleanup(t *testing.T) {
	m := runner.New(context.Background(), backend{confirm: true}, 2)
	t.Cleanup(m.Close)
	h := New(m, Options{})
	w := request(h, "POST", "/api/send", `{"name":"one","text":"hello"}`, nil, "http://pai.test")
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	s := wait(t, m, func(s runner.Snapshot) bool { return s.Pending != nil })
	// A failed deadline/write path must relinquish its subscriber slot.
	r := httptest.NewRequest("GET", "http://pai.test/api/events?name=one", nil)
	h.ServeHTTP(&failedDeadlineWriter{httptest.NewRecorder()}, r)
	for i := 0; i < 32; i++ {
		_, unsubscribe, err := m.Subscribe("one")
		if err != nil {
			t.Fatalf("leaked SSE subscription: %v", err)
		}
		defer unsubscribe()
	}
	w = request(h, "POST", "/api/cancel", `{"name":"one"}`, nil, "http://pai.test")
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	body, _ := json.Marshal(map[string]any{"name": "one", "prompt_id": s.Pending.ID, "text": "late"})
	w = request(h, "POST", "/api/reply", string(body), nil, "http://pai.test")
	if w.Code != 409 && w.Code != 404 {
		t.Fatalf("cancel left replyable prompt: %d", w.Code)
	}
}

type failedDeadlineWriter struct{ *httptest.ResponseRecorder }

func (*failedDeadlineWriter) SetWriteDeadline(time.Time) error {
	return errors.New("connection no longer writable")
}

func TestActionsAndSSEReconnect(t *testing.T) {
	m := runner.New(context.Background(), backend{confirm: true}, 2)
	t.Cleanup(m.Close)
	h := New(m, Options{})
	post := func(path, body string, status int) {
		t.Helper()
		w := request(h, "POST", "/api/"+path, body, nil, "http://pai.test")
		if w.Code != status {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	post("send", `{"name":"one","text":"hello"}`, 202)
	s := wait(t, m, func(s runner.Snapshot) bool { return s.Pending != nil })
	server := httptest.NewServer(h)
	defer server.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	// Disconnect/reconnect recovers the same replyable prompt and leaves it active.
	for i := 0; i < 2; i++ {
		response, err := client.Get(server.URL + "/api/events?name=one")
		if err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(response.Body).ReadString('\n')
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		var event runner.Event
		if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != "snapshot" || event.Snapshot.Pending == nil || event.Snapshot.Pending.ID != s.Pending.ID {
			t.Fatal(event)
		}
	}
	post("reply", `{"name":"one","prompt_id":"stale","text":"yes"}`, 409)
	body, _ := json.Marshal(map[string]any{"name": "one", "prompt_id": s.Pending.ID, "text": "target"})
	post("reply", string(body), 202)
	post("reply", string(body), 409)
	wait(t, m, func(s runner.Snapshot) bool { return s.State == "awaiting" })
	post("steer", `{"name":"one","text":"next"}`, 202)
	wait(t, m, func(s runner.Snapshot) bool { return s.State == "awaiting" && s.Queued == 0 })
	post("cancel", `{"name":"one"}`, 202)
	post("steer", `{"name":"one","text":"/help"}`, 400)
}
