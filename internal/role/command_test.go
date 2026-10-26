package role

import (
	"context"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

// fakeObserver records the events commands emit.
type fakeObserver struct {
	outputs    []string
	notices    []string
	sessions   []string
	toolCalls  []core.ToolCall
	compacting int
}

func (o *fakeObserver) Reason(string)              {}
func (o *fakeObserver) Done(string)                {}
func (o *fakeObserver) Terminate(string)           {}
func (o *fakeObserver) Ask(string)                 {}
func (o *fakeObserver) Awaiting()                  {}
func (o *fakeObserver) Compacting()                { o.compacting++ }
func (o *fakeObserver) User(string)                {}
func (o *fakeObserver) Reasoning(string)           {}
func (o *fakeObserver) Usage(core.Usage)           {}
func (o *fakeObserver) ToolCall(c core.ToolCall)   { o.toolCalls = append(o.toolCalls, c) }
func (o *fakeObserver) ToolOutput(string)          {}
func (o *fakeObserver) ToolResult(core.ToolResult) {}
func (o *fakeObserver) Separator()                 {}
func (o *fakeObserver) Session(name string)        { o.sessions = append(o.sessions, name) }
func (o *fakeObserver) Notice(text string)         { o.notices = append(o.notices, text) }
func (o *fakeObserver) Output(text string)         { o.outputs = append(o.outputs, text) }

func (o *fakeObserver) lastOutput() string {
	if len(o.outputs) == 0 {
		return ""
	}
	return o.outputs[len(o.outputs)-1]
}

func (o *fakeObserver) lastNotice() string {
	if len(o.notices) == 0 {
		return ""
	}
	return o.notices[len(o.notices)-1]
}

// fakeSessions records the storage calls commands make.
type fakeSessions struct {
	persisted []string
	created   []string
	roles     []string
	turns     int
}

func (s *fakeSessions) Persist(name string, turns []core.Turn) (core.Recorder, error) {
	s.persisted = append(s.persisted, name)
	s.turns = len(turns)
	return nil, nil
}

func (s *fakeSessions) New(name string) (core.Recorder, error) {
	s.created = append(s.created, name)
	return nil, nil
}

func (s *fakeSessions) SetRole(role string) error {
	s.roles = append(s.roles, role)
	return nil
}

// newTestCtx builds a command context against the real built-in devops role,
// with the user's config directory isolated so nothing leaks in.
func newTestCtx(t *testing.T) (*conversation, *Runtime, *fakeObserver) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	rp, err := chat.LoadRolePrompt("devops", "")
	if err != nil {
		t.Fatalf("load role: %v", err)
	}
	obs := &fakeObserver{}
	rt := &Runtime{Observer: obs}
	cfg := &config.UserConfig{DefaultRole: "devops", DefaultModel: "test:model"}
	conv := &conversation{rt: rt, cfg: cfg, rp: rp, ctx: context.Background()}
	return conv, rt, obs
}

// fakeProvider stands in for the model in commands that call it (e.g. /compact).
type fakeProvider struct {
	reply string
	got   provider.CompletionParams
	calls int
}

func (f *fakeProvider) Completion(_ context.Context, p provider.CompletionParams) (*provider.ChatCompletion, error) {
	f.calls++
	f.got = p
	return &provider.ChatCompletion{Choices: []provider.Choice{
		{Message: provider.Message{Role: provider.RoleAssistant, Content: f.reply}},
	}}, nil
}

func (f *fakeProvider) CompletionStream(context.Context, provider.CompletionParams) (<-chan provider.ChatCompletionChunk, <-chan error) {
	return nil, nil
}

// /compact summarizes the older turns and keeps the recent ones verbatim.
func TestCompactCommandSummarizesOldTurns(t *testing.T) {
	conv, rt, obs := newTestCtx(t)
	fp := &fakeProvider{reply: "ran df -h; disk is fine"}
	rt.Provider = fp
	conv.cfg.Context.KeepTurns = 1

	conv.history = []provider.Message{
		{Role: provider.RoleUser, Kind: core.KindInput, Content: "check disk"},
		{Role: provider.RoleAssistant, Kind: core.KindOutput, Content: `{"action":"tool","toolname":"execute","payload":"df -h","reason":"r"}`},
		{Role: provider.RoleUser, Kind: core.KindToolResult, Content: "[exec result]: exit code 0\nok"},
		{Role: provider.RoleUser, Kind: core.KindInput, Content: "now do more"},
		{Role: provider.RoleAssistant, Kind: core.KindOutput, Content: `{"action":"done","payload":"ok","reason":"r"}`},
	}

	if outcome, _ := dispatch(conv, "/compact"); outcome != cmdHandled {
		t.Fatalf("outcome = %v", outcome)
	}
	if fp.calls != 1 {
		t.Errorf("summarizer called %d times, want 1", fp.calls)
	}
	// The summarizer call does not stream, so the observer is told one is running.
	if obs.compacting != 1 {
		t.Errorf("compacting events = %d, want 1", obs.compacting)
	}
	if len(conv.history) != 3 {
		t.Fatalf("history = %d messages, want summary + 2 kept", len(conv.history))
	}
	if conv.history[0].Kind != core.KindSummary {
		t.Errorf("first message kind = %q, want summary", conv.history[0].Kind)
	}
	if !strings.Contains(conv.history[0].Content, "disk is fine") {
		t.Errorf("summary text missing: %q", conv.history[0].Content)
	}
	// The last whole turn (a user instruction plus its reply) is kept, not a
	// message count.
	if conv.history[1].Kind != core.KindInput || conv.history[1].Content != "now do more" {
		t.Errorf("the most recent turn was not kept verbatim: %+v", conv.history[1])
	}
	if obs.lastOutput() == "" {
		t.Error("expected a confirmation line")
	}

	// The checkpoint is recorded, so a later attach replays the summary instead
	// of the turns it stands in for.
	summaries := 0
	for _, tn := range rt.transcript {
		if tn.Kind == core.KindSummary {
			summaries++
			if !strings.Contains(tn.Content, "disk is fine") {
				t.Errorf("checkpoint content = %q", tn.Content)
			}
		}
	}
	if summaries != 1 {
		t.Errorf("recorded %d summary checkpoints, want 1", summaries)
	}
}

// With nothing old enough, /compact says so instead of calling the model.
func TestCompactCommandNothingToDo(t *testing.T) {
	conv, rt, obs := newTestCtx(t)
	fp := &fakeProvider{reply: "unused"}
	rt.Provider = fp
	conv.cfg.Context.KeepTurns = 5
	conv.history = []provider.Message{{Role: provider.RoleUser, Kind: core.KindInput, Content: "hi"}}

	if outcome, _ := dispatch(conv, "/compact"); outcome != cmdHandled {
		t.Fatalf("outcome = %v", outcome)
	}
	if fp.calls != 0 {
		t.Errorf("summarizer should not be called: %d calls", fp.calls)
	}
	if obs.compacting != 0 {
		t.Errorf("a no-op must not announce a compaction: %d", obs.compacting)
	}
	if !strings.Contains(obs.lastOutput(), "nothing to compact") {
		t.Errorf("output = %q", obs.lastOutput())
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		line  string
		name  string
		args  []string
		isCmd bool
	}{
		{"/help", "help", nil, true},
		{"/HELP", "help", nil, true},
		{"/role coder", "role", []string{"coder"}, true},
		{"/rename my-session", "rename", []string{"my-session"}, true},
		{"/", "", nil, true},
		{"hello", "", nil, false},
		{"//help", "", nil, false},
		// A path-like line parses as an unknown command; "//" is the escape for
		// sending one to the model literally.
		{"/dev/null please", "dev/null", []string{"please"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			name, args, isCmd := splitCommand(strings.TrimSpace(tc.line))
			if isCmd != tc.isCmd || name != tc.name {
				t.Errorf("splitCommand(%q) = (%q, %v, %v), want (%q, _, %v)",
					tc.line, name, args, isCmd, tc.name, tc.isCmd)
			}
			if strings.Join(args, ",") != strings.Join(tc.args, ",") {
				t.Errorf("args = %v, want %v", args, tc.args)
			}
		})
	}
}

func TestDispatchMessage(t *testing.T) {
	conv, _, _ := newTestCtx(t)

	outcome, msg := dispatch(conv, "  run df -h  ")
	if outcome != cmdMessage || msg != "run df -h" {
		t.Errorf("got (%v, %q), want a plain message", outcome, msg)
	}

	// A doubled slash escapes a literal leading slash.
	outcome, msg = dispatch(conv, "//not a command")
	if outcome != cmdMessage || msg != "/not a command" {
		t.Errorf("escape: got (%v, %q), want (/not a command)", outcome, msg)
	}
}

func TestDispatchErrorsAreHandledNotSent(t *testing.T) {
	cases := []struct {
		name   string
		line   string
		notice string
	}{
		{"unknown command", "/nope", "unknown command"},
		{"missing name", "/", "missing command name"},
		{"too many args", "/exit now", "usage: /exit"},
		{"too few args", "/rename", "usage: /rename <name>"},
		{"invalid role", "/role nothing", "unknown role"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conv, _, obs := newTestCtx(t)
			outcome, msg := dispatch(conv, tc.line)
			if outcome != cmdHandled || msg != "" {
				t.Fatalf("got (%v, %q), want (cmdHandled, \"\")", outcome, msg)
			}
			if !strings.Contains(obs.lastNotice(), tc.notice) {
				t.Errorf("notice = %q, want it to contain %q", obs.lastNotice(), tc.notice)
			}
		})
	}
}

func TestExitAndHelp(t *testing.T) {
	conv, _, obs := newTestCtx(t)

	if outcome, _ := dispatch(conv, "/exit"); outcome != cmdExit {
		t.Errorf("/exit = %v, want cmdExit", outcome)
	}
	if outcome, _ := dispatch(conv, "/quit"); outcome != cmdExit {
		t.Errorf("/quit alias = %v, want cmdExit", outcome)
	}

	if outcome, _ := dispatch(conv, "/help"); outcome != cmdHandled {
		t.Errorf("/help = %v, want cmdHandled", outcome)
	}
	// /help is generated from the table, so every command must appear.
	for _, s := range commandSpecs {
		if !strings.Contains(obs.lastOutput(), s.usage) {
			t.Errorf("/help output is missing %q:\n%s", s.usage, obs.lastOutput())
		}
	}
}

// /trusted reports both the run-only list and the config list.
func TestTrustedCommand(t *testing.T) {
	conv, rt, obs := newTestCtx(t)
	rt.TrustedCmds = []string{"make"}
	rt.TrustedPaths = []string{"/srv/run"}
	conv.cfg.TrustedCmds = []string{"ls"}
	conv.cfg.TrustedPaths = []string{"/srv/cfg"}
	if outcome, _ := dispatch(conv, "/trusted"); outcome != cmdHandled {
		t.Fatalf("outcome = %v", outcome)
	}
	out := obs.lastOutput()
	for _, want := range []string{"this run only", "commands: make", "paths:    /srv/run", "from config", "commands: ls", "paths:    /srv/cfg"} {
		if !strings.Contains(out, want) {
			t.Errorf("/trusted output missing %q:\n%s", want, out)
		}
	}
}

func TestRoleSwitch(t *testing.T) {
	conv, _, obs := newTestCtx(t)

	dispatch(conv, "/role")
	if !strings.Contains(obs.lastOutput(), "role: devops") {
		t.Errorf("/role output = %q", obs.lastOutput())
	}

	dispatch(conv, "/role coder")
	if conv.cfg.DefaultRole != "coder" || conv.rp.Name != "coder" {
		t.Errorf("role not switched: cfg=%q rp=%q", conv.cfg.DefaultRole, conv.rp.Name)
	}
	// coder has no remote tool; the switch must apply the role's capability set.
	for _, tl := range conv.rp.Tools {
		if tl.Name == "remote" {
			t.Error("coder should not have the remote tool")
		}
	}
}

// Switching role saves it to the session so a later attach keeps it.
func TestRoleSwitchSavesRole(t *testing.T) {
	conv, rt, _ := newTestCtx(t)
	sessions := &fakeSessions{}
	rt.Sessions = sessions

	dispatch(conv, "/role coder")
	if len(sessions.roles) != 1 || sessions.roles[0] != "coder" {
		t.Fatalf("saved roles = %v, want [coder]", sessions.roles)
	}
}

func TestNewWithoutSessionsClearsLocally(t *testing.T) {
	conv, rt, obs := newTestCtx(t)
	conv.history = []provider.Message{{Role: "user", Content: "hi"}}
	rt.transcript = []core.Turn{{Role: "user", Kind: "input", Content: "hi"}}

	if outcome, _ := dispatch(conv, "/new"); outcome != cmdHandled {
		t.Fatalf("outcome = %v", outcome)
	}
	if len(conv.history) != 0 || len(rt.transcript) != 0 {
		t.Errorf("conversation not cleared: history=%d transcript=%d", len(conv.history), len(rt.transcript))
	}
	if len(obs.sessions) != 1 || obs.sessions[0] != "" {
		t.Errorf("session events = %v, want a single empty name", obs.sessions)
	}

	// Naming a session needs storage.
	dispatch(conv, "/new work")
	if !strings.Contains(obs.lastNotice(), "unavailable") {
		t.Errorf("notice = %q, want it to mention unavailable storage", obs.lastNotice())
	}
}

func TestNewWithSessions(t *testing.T) {
	conv, rt, obs := newTestCtx(t)
	sessions := &fakeSessions{}
	rt.Sessions = sessions

	dispatch(conv, "/new work")
	if len(sessions.created) != 1 || sessions.created[0] != "work" {
		t.Errorf("created = %v, want [work]", sessions.created)
	}
	if conv.session != "work" {
		t.Errorf("session = %q, want work", conv.session)
	}
	if len(obs.sessions) != 1 || obs.sessions[0] != "work" {
		t.Errorf("session events = %v", obs.sessions)
	}
}

func TestRenamePersistsTranscript(t *testing.T) {
	conv, rt, obs := newTestCtx(t)
	sessions := &fakeSessions{}
	rt.Sessions = sessions
	rt.transcript = []core.Turn{
		{Role: "user", Kind: "input", Content: "a"},
		{Role: "assistant", Kind: "output", Content: "b"},
	}

	dispatch(conv, "/rename my-session")

	if len(sessions.persisted) != 1 || sessions.persisted[0] != "my-session" {
		t.Errorf("persisted = %v, want [my-session]", sessions.persisted)
	}
	if sessions.turns != 2 {
		t.Errorf("backfilled %d turns, want 2", sessions.turns)
	}
	if conv.session != "my-session" {
		t.Errorf("session = %q", conv.session)
	}
	if len(obs.sessions) != 1 || obs.sessions[0] != "my-session" {
		t.Errorf("session events = %v", obs.sessions)
	}

	// Renaming to the current name is a no-op, not an error.
	dispatch(conv, "/rename my-session")
	if !strings.Contains(obs.lastOutput(), "already named") {
		t.Errorf("output = %q", obs.lastOutput())
	}
}

func TestRenameWithoutSessions(t *testing.T) {
	conv, _, obs := newTestCtx(t)
	dispatch(conv, "/rename work")
	if !strings.Contains(obs.lastNotice(), "unavailable") {
		t.Errorf("notice = %q", obs.lastNotice())
	}
}
