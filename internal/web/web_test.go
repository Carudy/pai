package web

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strings"
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

type backend struct{ confirm bool }

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
	model string
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
func (b *modelBackend) History(name string, offset, limit int) (runner.History, error) {
	h, err := b.backend.History(name, offset, limit)
	h.Model = b.model
	return h, err
}

func TestModelsAPI(t *testing.T) {
	b := &modelBackend{}
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
	if err := m.Reply("one", pending.Pending.ID, "target", false); err != nil {
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
		"model-form": "session-title-row", "apply-model": "model-form",
		"workspace": "header", "state": "status-row", "token-usage": "header",
	} {
		if parents[child] != parent {
			t.Errorf("%s parent = %q, want %q", child, parents[child], parent)
		}
	}
	if !strings.Contains(page, `aria-labelledby="pending-title"`) {
		t.Error("pending overlay missing accessible title reference")
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
		if event.Snapshot.Pending == nil || event.Snapshot.Pending.Tool == nil || *event.Snapshot.Pending.Tool != *s.Pending.Tool {
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
	h, m := setup(t, "")
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
	h, m := setup(t, "")
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
