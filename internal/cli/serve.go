package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/provider"
	"github.com/Carudy/pai/internal/runner"
	"github.com/Carudy/pai/internal/session"
	"github.com/Carudy/pai/internal/tui"
	"github.com/Carudy/pai/internal/web"
)

func serveHelp() string {
	return `FLAGS
  --ip, -ip <IP>                 Bind IP (default 127.0.0.1; no hostnames)
  --port, -p <port>              Bind port (default 9384)
  --max-active-sessions <n>      Maximum live workers (default 2)
  --activity-log                Log tool calls/lifecycle to stdout (default false)
  --token-file <path>            Token file; overrides PAI_SERVE_TOKEN
  --public-origin <origin>       Browser origin, e.g. https://pai.example.com
  --help, -h                    Show help

Non-loopback binds and --public-origin require a token of at least 32 characters.
For reverse proxies, preserving Host is recommended. Set --public-origin when
terminating TLS or changing Host; forwarded headers are never trusted.
Activity logs omit reasoning and tool output; commands may contain sensitive data.
Remote access requires TLS or a trusted TLS terminator. Clients can run tools
with this process's permissions. Sessions use their saved workspace; new sessions default to the starting cwd.`
}

type serveFlags struct {
	ip              string
	port, maxActive int
	tokenFile       string
	publicOrigin    string
	help            bool
	activityLog     bool
}

func parseServeFlags(args []string) (serveFlags, error) {
	var f serveFlags
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&f.ip, "ip", "127.0.0.1", "bind IP")
	fs.IntVar(&f.port, "port", 9384, "bind port")
	fs.IntVar(&f.port, "p", 9384, "bind port")
	fs.IntVar(&f.maxActive, "max-active-sessions", 2, "maximum live workers")
	fs.StringVar(&f.tokenFile, "token-file", "", "token file")
	fs.StringVar(&f.publicOrigin, "public-origin", "", "canonical browser origin")
	fs.BoolVar(&f.activityLog, "activity-log", false, "log activity to stdout")
	fs.BoolVar(&f.help, "help", false, "help")
	fs.BoolVar(&f.help, "h", false, "help")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if f.help {
		return f, nil
	}
	if fs.NArg() != 0 {
		return f, errors.New("serve accepts no positional arguments")
	}
	if net.ParseIP(f.ip) == nil {
		return f, errors.New("bind address must be an IP literal")
	}
	if f.port < 1 || f.port > 65535 {
		return f, errors.New("port must be between 1 and 65535")
	}
	if f.maxActive < 1 {
		return f, errors.New("max-active-sessions must be positive")
	}
	publicOriginSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "public-origin" {
			publicOriginSet = true
		}
	})
	if publicOriginSet {
		origin, err := canonicalServeOrigin(f.publicOrigin)
		if err != nil {
			return f, err
		}
		f.publicOrigin = origin
	}
	return f, nil
}

func canonicalServeOrigin(raw string) (string, error) {
	invalid := errors.New("public-origin must be an http/https origin without path, query, fragment, or userinfo")
	u, err := url.Parse(raw)
	if err != nil {
		return "", invalid
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" || strings.ContainsAny(raw, "?#") {
		return "", invalid
	}
	hostname := u.Hostname()
	if hostname == "" {
		return "", invalid
	}

	if net.ParseIP(hostname) == nil {
		if strings.ContainsAny(u.Host, "[]") {
			return "", invalid
		}
		for _, label := range strings.Split(hostname, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", invalid
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
					return "", invalid
				}
			}
		}
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", invalid
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", invalid
		}
	}
	// Reject unbracketed IPv6 and other authority spellings browsers cannot use.
	host := strings.ToLower(hostname)
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" {
		host += ":" + port
	}
	if !strings.EqualFold(u.Host, host) {
		return "", invalid
	}
	return u.Scheme + "://" + host, nil
}

func serveToken(f serveFlags) (string, error) {
	token := os.Getenv("PAI_SERVE_TOKEN")
	if f.tokenFile != "" {
		data, err := os.ReadFile(f.tokenFile)
		if err != nil {
			return "", fmt.Errorf("read token file: %w", err)
		}
		token = string(data)
	}
	token = strings.TrimSpace(token)
	if !utf8.ValidString(token) || strings.IndexFunc(token, unicode.IsSpace) >= 0 || strings.IndexFunc(token, unicode.IsControl) >= 0 {
		return "", errors.New("token must contain valid non-whitespace characters")
	}
	ip := net.ParseIP(f.ip)
	if ip == nil {
		return "", errors.New("bind address must be an IP literal")
	}
	if (!ip.IsLoopback() || f.publicOrigin != "") && utf8.RuneCountInString(token) < 32 {
		return "", errors.New("non-loopback bind or public-origin requires a token of at least 32 characters")
	}
	return token, nil
}

func runServe(ctx context.Context, args []string, stdout io.Writer, log *tui.Logger) int {
	f, err := parseServeFlags(args)
	if err != nil {
		log.Errorf("serve: %v\n", err)
		return 1
	}
	if f.help {
		printCommandHelp(stdout, findCommand("serve"))
		return 0
	}
	token, err := serveToken(f)
	if err != nil {
		log.Errorf("serve: %v\n", err)
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		log.Errorf("serve cwd: %v\n", err)
		return 1
	}
	store, err := session.Open()
	if err != nil {
		log.Errorf("open session store: %v\n", err)
		return 1
	}
	defer store.Close()
	listener, err := net.Listen("tcp", net.JoinHostPort(f.ip, fmt.Sprint(f.port)))
	if err != nil {
		log.Errorf("listen: %v\n", err)
		return 1
	}
	defer listener.Close()
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	manager := runner.New(runCtx, &serveBackend{store: store, cwd: cwd}, f.maxActive)
	defer manager.Close()
	handler := web.New(manager, web.Options{Token: token, PublicOrigin: f.publicOrigin})
	// Origin checks alone do not prevent DNS rebinding on unauthenticated loopback.
	host := listener.Addr().String()
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" && r.Host != host {
				http.Error(w, "invalid host", http.StatusForbidden)
				return
			}
			handler.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	result := make(chan error, 1)
	fmt.Fprintf(stdout, "Serving http://%s (workspace %s)\n", listener.Addr(), cwd)
	if f.activityLog {
		stopLog, logErr := startServeActivityLog(manager, stdout)
		if logErr != nil {
			log.Errorf("activity log: %v\n", logErr)
			return 1
		}
		defer stopLog()
	}
	go func() { result <- server.Serve(listener) }()
	select {
	case err = <-result:
	case <-runCtx.Done():
	}
	// Closing the manager first unblocks pending questions and SSE subscribers.
	manager.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		server.Close()
		log.Errorf("shutdown server: %v\n", shutdownErr)
		return 1
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Errorf("serve: %v\n", err)
		return 1
	}
	return 0
}

func serveActivityLine(a runner.Activity) string {
	label := a.Event.Type
	parts := []string{label}
	switch label {
	case "tool_call":
		c, ok := a.Event.Data.(core.ToolCall)
		if !ok {
			return ""
		}
		parts = []string{"tool_exec", c.Name, c.Target}
		// Only commands are useful here; file content, diffs and queries stay private.
		if c.Name == "execute" || c.Name == "remote" {
			parts = append(parts, c.Detail)
		}
	case "prompt":
		if p, ok := a.Event.Data.(runner.Prompt); ok {
			parts = append(parts, p.Kind)
		}
	case "done", "terminate", "awaiting", "stopped", "prompt_cancelled", "prompt_replied":
	default:
		return ""
	}
	return "[" + serveActivityText(a.Name) + "]: " + serveActivityText(strings.Join(parts, " ")) + "\n"
}

func serveActivityText(s string) string {
	// Escape terminal controls and collapse newlines to keep each event one line.
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	runes := []rune(strings.Join(strings.Fields(s), " "))
	if len(runes) > 512 {
		return string(runes[:512]) + "…"
	}
	return string(runes)
}

func startServeActivityLog(m *runner.Manager, stdout io.Writer) (func(), error) {
	activities, unsubscribe, err := m.SubscribeActivities()
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for a := range activities {
			if line := serveActivityLine(a); line != "" {
				if _, err := io.WriteString(stdout, line); err != nil {
					unsubscribe()
					return
				}
			}
		}
	}()
	return func() {
		unsubscribe()
		// io.Writer has no cancellation contract. Drain normally, but do not
		// hold server shutdown hostage to a blocked stdout write.
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		}
	}, nil
}

// The same mutex protects API reads, preparation, and recorder appends on both
// backends; a worker never owns the store or changes the process workspace.
type serveBackend struct {
	mu    sync.Mutex
	store session.Store
	cwd   string
}

func serveMeta(m session.Meta) runner.Meta {
	return runner.Meta{Name: m.Name, Role: m.Role, Model: m.Model, Cwd: m.Cwd, UpdatedAt: m.UpdatedAt}
}

func (b *serveBackend) List() ([]runner.Meta, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	metas, err := b.store.List()
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	out := make([]runner.Meta, 0, len(metas))
	for _, m := range metas {
		out = append(out, serveMeta(m))
	}
	return out, nil
}

func (b *serveBackend) History(name string, offset, limit int) (runner.History, error) {
	if !session.ValidName(name) {
		return runner.History{}, errors.New("invalid session name")
	}
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 200 {
		return runner.History{}, errors.New("invalid history pagination")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s, err := b.store.Get(name)
	if errors.Is(err, session.ErrNotFound) {
		return runner.History{}, runner.ErrNotFound
	}
	if err != nil {
		return runner.History{}, fmt.Errorf("get history: %w", err)
	}
	start := min(offset, len(s.Turns))
	end := min(start+limit, len(s.Turns))
	turns := append([]core.Turn{}, s.Turns[start:end]...)
	return runner.History{Meta: serveMeta(s.Meta), Turns: turns, Total: len(s.Turns)}, nil
}

func (b *serveBackend) Prepare(name string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), error) {
	cfg, p, rec, history, cleanup, _, err := b.PrepareWorkspace(name, "")
	return cfg, p, rec, history, cleanup, err
}

func (b *serveBackend) Models() ([]string, string, error) {
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load config: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	metas, err := b.store.List()
	if err != nil {
		return nil, "", fmt.Errorf("list session models: %w", err)
	}
	seen := map[string]bool{cfg.DefaultModel: true}
	for _, meta := range metas {
		if meta.Model != "" {
			seen[meta.Model] = true
		}
	}
	models := make([]string, 0, len(seen))
	for model := range seen {
		models = append(models, model)
	}
	sort.Strings(models)
	return models, cfg.DefaultModel, nil
}

func (b *serveBackend) SetModel(name, model string) error {
	if !session.ValidName(name) {
		return errors.New("invalid session name")
	}
	providerName, modelName, found := strings.Cut(model, ":")
	providerName, modelName = strings.TrimSpace(providerName), strings.TrimSpace(modelName)
	model = providerName + ":" + modelName
	if !found || providerName == "" || strings.TrimSpace(modelName) == "" || strings.IndexFunc(model, unicode.IsSpace) >= 0 || strings.IndexFunc(model, unicode.IsControl) >= 0 {
		return errors.New("model must be provider:model with nonempty names and no whitespace")
	}
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	pc, ok := cfg.ProvidersConfigs[providerName]
	if !ok {
		return fmt.Errorf("provider %q is not configured", providerName)
	}
	if _, err := provider.CreateClient(providerName, pc.APIKey, modelName, pc.BaseURL); err != nil {
		return fmt.Errorf("validate provider: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.store.SetModel(name, model); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return runner.ErrNotFound
		}
		return fmt.Errorf("set session model: %w", err)
	}
	return nil
}

func (b *serveBackend) Roles() ([]string, string, error) {
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load config: %w", err)
	}
	return prompts.RoleNames(), cfg.DefaultRole, nil
}

func (b *serveBackend) Create(name, cwd string) (runner.Meta, error) {
	return b.CreateWithRole(name, cwd, "")
}

func (b *serveBackend) CreateWithRole(name, cwd, role string) (runner.Meta, error) {
	if !session.ValidName(name) {
		return runner.Meta{}, errors.New("invalid session name")
	}
	if cwd == "" {
		cwd = b.cwd
	}
	dir, err := runner.ResolveWorkingDir(cwd)
	if err != nil {
		return runner.Meta{}, err
	}
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return runner.Meta{}, fmt.Errorf("load config: %w", err)
	}
	if role == "" {
		role = cfg.DefaultRole
	}
	valid := false
	for _, name := range prompts.RoleNames() {
		if name == role {
			valid = true
			break
		}
	}
	if !valid {
		return runner.Meta{}, fmt.Errorf("unknown role %q (available: %s)", role, strings.Join(prompts.RoleNames(), ", "))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s, err := b.store.Create(session.Meta{Name: name, Role: role, Model: cfg.DefaultModel, Cwd: dir})
	if err != nil {
		return runner.Meta{}, fmt.Errorf("create session: %w", err)
	}
	return serveMeta(s.Meta), nil
}

func (b *serveBackend) PrepareWorkspace(name, cwd string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), string, error) {
	fail := func(err error) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), string, error) {
		return nil, nil, nil, nil, nil, "", err
	}
	if !session.ValidName(name) {
		return fail(errors.New("invalid session name"))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s, err := b.store.Get(name)
	if err != nil && !errors.Is(err, session.ErrNotFound) {
		return fail(fmt.Errorf("get session: %w", err))
	}
	exists := err == nil
	if exists {
		if cwd != "" {
			requested, err := runner.ResolveWorkingDir(cwd)
			if err != nil {
				return fail(err)
			}
			stored, err := runner.ResolveWorkingDir(s.Meta.Cwd)
			if err != nil {
				return fail(err)
			}
			if requested != stored {
				return fail(errors.New("requested working directory differs from session workspace"))
			}
		}
		cwd = s.Meta.Cwd
	} else if cwd == "" {
		cwd = b.cwd
	}
	cwd, err = runner.ResolveWorkingDir(cwd)
	if err != nil {
		return fail(err)
	}
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return fail(fmt.Errorf("load config: %w", err))
	}
	if exists {
		cfg.DefaultRole = s.Meta.Role
		cfg.SetModel(s.Meta.Model)
	}
	if cfg.Provider == "" || cfg.Model == "" {
		return fail(errors.New("model must be provider:model"))
	}
	custom, err := config.LoadCustomPrompt(cfg.DefaultRole)
	if err != nil {
		return fail(fmt.Errorf("load custom prompt: %w", err))
	}
	cfg.CustomPrompt = custom
	if _, err := chat.LoadRolePromptAt(cfg.DefaultRole, custom, cwd); err != nil {
		return fail(fmt.Errorf("load role: %w", err))
	}
	pc := cfg.ProvidersConfigs[cfg.Provider]
	client, err := provider.CreateClient(cfg.Provider, pc.APIKey, cfg.Model, pc.BaseURL)
	if err != nil {
		return fail(fmt.Errorf("create provider: %w", err))
	}
	if !exists {
		s, err = b.store.Create(session.Meta{Name: name, Role: cfg.DefaultRole, Model: cfg.DefaultModel, Cwd: cwd})
		if err != nil {
			return fail(fmt.Errorf("create session: %w", err))
		}
	}
	return cfg, client, &serveRecorder{backend: b, name: name}, toMessages(replayTurns(s.Turns, cfg.SessionMaxTurns)), func() {}, cwd, nil
}

type serveRecorder struct {
	backend *serveBackend
	name    string
}

func (r *serveRecorder) AppendTurn(t core.Turn) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if err := r.backend.store.Append(r.name, t); err != nil {
		return fmt.Errorf("append session turn: %w", err)
	}
	return nil
}

var _ runner.Backend = (*serveBackend)(nil)
var _ runner.ModelBackend = (*serveBackend)(nil)
