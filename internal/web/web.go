// Package web exposes a runner manager through a same-origin browser UI.
package web

import (
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/runner"
)

// Options configures browser authentication. An empty Token disables authentication;
// the composition root must permit this only on an explicitly loopback listener.
type Options struct {
	Token string
	// PublicOrigin is the canonical browser origin (scheme://host[:port]) when
	// a proxy changes the request's host or terminates TLS. The CLI validates it.
	PublicOrigin string
	// ServerCwd is the server's launch directory. New sessions default to it and
	// the UI prefills its working-directory field with it. It is a convenience
	// hint, not a sandbox.
	ServerCwd string
}

//go:embed index.html style.css app.js vendor/*.js
var assets embed.FS

const cookieName = "pai_session"
const sessionLifetime = 12 * time.Hour
const requestLimit = 64 << 10

type handler struct {
	manager      *runner.Manager
	token        string
	publicOrigin string
	serverCwd    string
	mu           sync.Mutex
	sessions     map[[32]byte]time.Time
}

// New returns a self-contained handler. It neither owns nor closes m. Configure
// listener security and server header/idle timeouts at the composition root.
func New(m *runner.Manager, options Options) http.Handler {
	return &handler{manager: m, token: options.Token, publicOrigin: options.PublicOrigin, serverCwd: options.ServerCwd, sessions: make(map[[32]byte]time.Time)}
}

// acceptsGzip reports whether the client advertised gzip. Only static assets are
// compressed: API JSON is small, and SSE must stream unbuffered.
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]), "gzip") {
			return true
		}
	}
	return false
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(10 * time.Second))
	_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if r.URL.RawQuery != "" && (r.URL.Query().Has("token") || r.URL.Query().Has("access_token")) {
		fail(w, http.StatusBadRequest, "URL credentials are not accepted")
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if r.Method != http.MethodGet {
			fail(w, 405, "method not allowed")
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if name != "index.html" && name != "app.js" && name != "style.css" && name != "vendor/marked.min.js" && name != "vendor/purify.min.js" {
			fail(w, 404, "not found")
			return
		}
		data, err := assets.ReadFile(name)
		if err != nil {
			fail(w, 500, "asset unavailable")
			return
		}
		switch name {
		case "index.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case "app.js", "vendor/marked.min.js", "vendor/purify.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		default:
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		}
		w.Header().Add("Vary", "Accept-Encoding")
		if acceptsGzip(r) {
			// Assets are embedded in readable source form (hand-editable); gzip
			// gives the small transfer a minified asset would, with no build step.
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			_, _ = gz.Write(data)
			_ = gz.Close()
			return
		}
		_, _ = w.Write(data)
		return
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		fail(w, 403, "cross-site request rejected")
		return
	}
	if r.Method != http.MethodGet && !h.sameOrigin(r) {
		fail(w, 403, "same-origin request required")
		return
	}
	if r.URL.Path == "/api/login" {
		if r.Method != http.MethodPost {
			fail(w, 405, "method not allowed")
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		if !decode(w, r, &body) {
			return
		}
		got, want := sha256.Sum256([]byte(body.Token)), sha256.Sum256([]byte(h.token))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			fail(w, 401, "invalid token")
			return
		}
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			fail(w, 500, "credential generation failed")
			return
		}
		credential := hex.EncodeToString(b)
		key := sha256.Sum256([]byte(credential))
		now := time.Now()
		h.mu.Lock()
		for k, expires := range h.sessions {
			if !expires.After(now) {
				delete(h.sessions, k)
			}
		}
		if len(h.sessions) >= 128 {
			h.mu.Unlock()
			fail(w, 429, "too many browser sessions")
			return
		}
		h.sessions[key] = now.Add(sessionLifetime)
		h.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: credential, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || strings.HasPrefix(h.publicOrigin, "https://"), MaxAge: int(sessionLifetime.Seconds())})
		respond(w, 200, map[string]bool{"ok": true})
		return
	}
	if !h.authenticated(r) {
		fail(w, 401, "login required")
		return
	}
	switch r.URL.Path {
	case "/api/models":
		if !method(w, r, http.MethodGet) {
			return
		}
		models, defaultModel, err := h.manager.Models()
		if err != nil {
			managerError(w, err)
			return
		}
		if models == nil {
			models = []string{}
		}
		respond(w, 200, map[string]any{"models": models, "default_model": defaultModel})
	case "/api/model":
		if !method(w, r, http.MethodPost) {
			return
		}
		var body struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		}
		if !decode(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.Name) == "" || len(body.Name) > 256 {
			fail(w, 400, "session name must contain 1–256 bytes")
			return
		}
		if err := h.manager.SetModel(body.Name, body.Model); err != nil {
			managerError(w, err)
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
	case "/api/roles":
		if !method(w, r, http.MethodGet) {
			return
		}
		roles, defaultRole, err := h.manager.Roles()
		if err != nil {
			managerError(w, err)
			return
		}
		if roles == nil {
			roles = []string{}
		}
		respond(w, 200, map[string]any{"roles": roles, "default_role": defaultRole})
	case "/api/sessions":
		if !method(w, r, http.MethodGet) {
			return
		}
		offset, limit, ok := pagination(w, r)
		if !ok {
			return
		}
		list, err := h.manager.List()
		if err != nil {
			managerError(w, err)
			return
		}
		total := len(list)
		if offset > total {
			offset = total
		}
		end := offset + limit
		if end > total {
			end = total
		}
		respond(w, 200, map[string]any{"sessions": list[offset:end], "total": total, "default_cwd": h.serverCwd})
	case "/api/history":
		if !method(w, r, http.MethodGet) {
			return
		}
		offset, limit, ok := pagination(w, r)
		if !ok {
			return
		}
		history, err := h.manager.History(r.URL.Query().Get("name"), offset, limit)
		if err != nil {
			managerError(w, err)
			return
		}
		respond(w, 200, history)
	case "/api/snapshot":
		if !method(w, r, http.MethodGet) {
			return
		}
		snapshot, err := h.manager.Snapshot(r.URL.Query().Get("name"))
		if err != nil {
			managerError(w, err)
			return
		}
		respond(w, 200, snapshot)
	case "/api/events":
		if !method(w, r, http.MethodGet) {
			return
		}
		h.events(w, r)
	case "/api/create":
		if !method(w, r, http.MethodPost) {
			return
		}
		var body struct {
			Name       string `json:"name"`
			WorkingDir string `json:"working_dir"`
			Role       string `json:"role"`
		}
		if !decode(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.Name) == "" || len(body.Name) > 256 {
			fail(w, 400, "session name must contain 1–256 bytes")
			return
		}
		meta, err := h.manager.CreateWithRole(body.Name, body.WorkingDir, body.Role)
		if err != nil {
			managerError(w, err)
			return
		}
		respond(w, http.StatusCreated, meta)
	case "/api/send", "/api/steer", "/api/cancel", "/api/reply":
		if !method(w, r, http.MethodPost) {
			return
		}
		var body struct {
			Name       string `json:"name"`
			WorkingDir string `json:"working_dir"`
			Text       string `json:"text"`
			PromptID   string `json:"prompt_id"`
			Choice     string `json:"choice"`
		}
		if !decode(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.Name) == "" || len(body.Name) > 256 {
			fail(w, 400, "session name must contain 1–256 bytes")
			return
		}
		var err error
		switch r.URL.Path {
		case "/api/send":
			err = h.manager.SendWithWorkspace(body.Name, body.Text, body.WorkingDir)
		case "/api/steer":
			err = h.manager.Steer(body.Name, body.Text)
		case "/api/cancel":
			err = h.manager.Cancel(body.Name)
		case "/api/reply":
			choice, ok := trustChoice(body.Choice)
			if !ok {
				fail(w, 400, "invalid trust choice")
				return
			}
			err = h.manager.Reply(body.Name, body.PromptID, body.Text, choice)
		}
		if err != nil {
			managerError(w, err)
			return
		}
		respond(w, 202, map[string]bool{"ok": true})
	default:
		fail(w, 404, "not found")
	}
}

func parseOrigin(value string) (*url.URL, bool) {
	origin, err := url.Parse(value)
	return origin, err == nil && (origin.Scheme == "http" || origin.Scheme == "https") && origin.Host != "" && origin.User == nil && origin.Path == "" && origin.Opaque == "" && !origin.ForceQuery && origin.RawQuery == "" && !strings.Contains(value, "#")
}
func (h *handler) sameOrigin(r *http.Request) bool {
	origin, valid := parseOrigin(r.Header.Get("Origin"))
	if !valid {
		return false
	}
	scheme, host := "http", r.Host
	if r.TLS != nil {
		scheme = "https"
	}
	if h.publicOrigin != "" {
		public, valid := parseOrigin(h.publicOrigin)
		// Invalid explicit configuration must never fall back to request headers.
		if !valid {
			return false
		}
		scheme, host = public.Scheme, public.Host
	}
	return origin.Scheme == scheme && strings.EqualFold(origin.Host, host)
}
func (h *handler) authenticated(r *http.Request) bool {
	if h.token == "" {
		return true
	}
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	key := sha256.Sum256([]byte(cookie.Value))
	h.mu.Lock()
	defer h.mu.Unlock()
	expires, ok := h.sessions[key]
	if ok && !expires.After(time.Now()) {
		delete(h.sessions, key)
		return false
	}
	return ok
}
func method(w http.ResponseWriter, r *http.Request, want string) bool {
	if r.Method != want {
		w.Header().Set("Allow", want)
		fail(w, 405, "method not allowed")
		return false
	}
	return true
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		fail(w, 415, "application/json required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, requestLimit)
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		var extra any
		if dec.Decode(&extra) != io.EOF {
			err = errors.New("multiple JSON values")
		}
	}
	if err != nil {
		var limit *http.MaxBytesError
		status := 400
		if errors.As(err, &limit) {
			status = 413
		}
		fail(w, status, "invalid or oversized JSON body")
		return false
	}
	return true
}
func pagination(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	offset, limit := 0, 100
	for key, dest := range map[string]*int{"offset": &offset, "limit": &limit} {
		if text := r.URL.Query().Get(key); text != "" {
			n, err := strconv.Atoi(text)
			if err != nil || n < 0 || n > 1000000 {
				fail(w, 400, "invalid pagination")
				return 0, 0, false
			}
			*dest = n
		}
	}
	if limit < 1 || limit > 200 {
		fail(w, 400, "limit must be 1–200")
		return 0, 0, false
	}
	return offset, limit, true
}

// trustChoice maps the reply's choice field onto a core.TrustChoice. An empty
// value is the safe default (deny), so a client that omits it never approves a
// command by accident.
func trustChoice(s string) (core.TrustChoice, bool) {
	switch s {
	case "", "deny":
		return core.TrustDeny, true
	case "once":
		return core.TrustOnce, true
	case "session":
		return core.TrustSession, true
	case "always":
		return core.TrustPersist, true
	}
	return core.TrustDeny, false
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
func managerError(w http.ResponseWriter, err error) {
	status := 400
	switch {
	case errors.Is(err, runner.ErrNotFound):
		status = 404
	case errors.Is(err, runner.ErrPrompt), errors.Is(err, runner.ErrBusy):
		status = 409
	case errors.Is(err, runner.ErrCapacity), errors.Is(err, runner.ErrQueueFull):
		status = 429
	case errors.Is(err, runner.ErrClosed):
		status = 503
	}
	fail(w, status, err.Error())
}
func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	events, unsubscribe, err := h.manager.Subscribe(r.URL.Query().Get("name"))
	if err != nil {
		managerError(w, err)
		return
	}
	defer unsubscribe()
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	// Expiring connections bound credential lifetime; reconnect gets a fresh snapshot.
	lifetime := time.NewTimer(30 * time.Minute)
	defer lifetime.Stop()
	for {
		var frame string
		select {
		case <-r.Context().Done():
			return
		case <-lifetime.C:
			return
		case <-ticker.C:
			frame = ": heartbeat\n\n"
		case event, ok := <-events:
			if !ok {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				return
			}
			frame = "data: " + string(data) + "\n\n"
		}
		if rc.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
			return
		}
		if _, err := io.WriteString(w, frame); err != nil {
			return
		}
		if rc.Flush() != nil {
			return
		}
	}
}
