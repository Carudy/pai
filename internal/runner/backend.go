// Package runner hosts transport-independent, concurrent role runtimes.
package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

type Meta struct {
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Model     string    `json:"model"`
	Cwd       string    `json:"cwd"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// ID is the immutable identity events, watchers, mail, and tasks reference.
	// Name remains the display key used by the CLI and web routes.
	ID string `json:"id,omitempty"`
	// Turns is the stored turn count, so a remote listing shows the same column a
	// local one does without reading the transcript.
	Turns int `json:"turns,omitempty"`
	// Mode is "chat" or "service"; State is the last persisted runtime state.
	Mode     string     `json:"mode,omitempty"`
	State    string     `json:"state,omitempty"`
	NextWake *time.Time `json:"next_wake,omitempty"`
	// TokenScale converts PAI's token estimate into the provider's own accounting,
	// learned from the session's last request. Zero means uncalibrated.
	TokenScale float64 `json:"token_scale,omitempty"`
}
type History struct {
	Meta
	Turns []core.Turn `json:"turns"`
	Total int         `json:"total"`
}

// ContextView is the message list a session would send on its next request: the
// system Head, one Card per stored turn, and the system Tail, plus the model and
// compaction settings in effect. The context inspector renders it.
//
// Cards is a window over the full list, not all of it: Offset is the index of
// its first card and Total is the number of stored turns, so a client can walk
// back through a long transcript instead of receiving all of it at once.
type ContextView struct {
	Head   string          `json:"head"`
	Tail   string          `json:"tail"`
	Cards  []ContextCard   `json:"cards"`
	Model  string          `json:"model"`
	Config ContextSettings `json:"config"`
	Total  int             `json:"total"`
	Offset int             `json:"offset"`
	// EstimatedTokens is the size this projection would send, from chat's token
	// estimate (calibrated when the session has reported usage) — the same figure the
	// loop seeds its summarization budget with on resume. It lets a client show a
	// context size for a session no run has reported usage for yet. It is an estimate,
	// not a measurement, and it is computed over every turn regardless of the window.
	EstimatedTokens int `json:"estimated_tokens"`
	// EstimateScale is the factor applied to the raw estimate to reach that figure, from
	// the session's last reported request. 0/1 means the figure is uncalibrated.
	EstimateScale float64 `json:"estimate_scale,omitempty"`
	// CompactTurns is how many user turns a compaction would fold into a summary,
	// and UserTurns how many the replay holds. Zero CompactTurns means a manual
	// compaction has nothing to do, so a client can explain that instead of
	// offering a control that silently does nothing.
	CompactTurns int `json:"compact_turns"`
	UserTurns    int `json:"user_turns"`
}

// ContextCard is one stored turn's place in that list. Sent is empty when the
// turn is not sent at all; State says why (see chat's State* constants).
type ContextCard struct {
	Seq       int    `json:"seq"`
	Role      string `json:"role"`
	Kind      string `json:"kind"`
	Original  string `json:"original"`
	Sent      string `json:"sent"`
	State     string `json:"state"`
	Essential bool   `json:"essential,omitempty"`
}

// ContextSettings is the compaction configuration applied to a session, so a
// client can show the knobs that produced the view.
type ContextSettings struct {
	KeepTurns        int `json:"keep_turns"`
	ElideAfterTokens int `json:"elide_after_tokens"`
	ElideMinBytes    int `json:"elide_min_bytes"`
	ElideHeadLines   int `json:"elide_head_lines"`
	ElideTailLines   int `json:"elide_tail_lines"`
	MaxTurns         int `json:"max_turns"`
}

// Prepare transfers ownership of its resources to the worker. Cleanup is called
// exactly once, including when Prepare returns an error with a cleanup function.
// Backend methods must be concurrency-safe; Prepare must return promptly.
type Backend interface {
	List() ([]Meta, error)
	History(name string, offset, limit int) (History, error)
	Prepare(name string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), error)
}

// The interfaces below are OPTIONAL capability ports. The manager type-asserts
// each one, so a host implements only the subset its backend supports; a method
// made mandatory on Backend would break backends that cannot provide it.

// ServiceEventBackend is the durable half of a resident session. Events
// reference the immutable session id; metadata such as the next wake is still
// keyed by the display name. A backend without it can still run an ephemeral
// service, but events are not persisted, acked, or retried.
type ServiceEventBackend interface {
	AppendEvent(sessionID string, e core.Event) (int64, error)
	AckEvents(sessionID string, ids []int64) error
	// NackEvent returns an event to pending with retryAfter, or moves it to the
	// dead state when dead is true. The runner owns the retry policy; the backend
	// only persists the decision.
	NackEvent(sessionID string, id int64, cause error, retryAfter time.Duration, dead bool) error
	DueEvents(sessionID string, now time.Time, limit int) ([]core.Event, error)
	// NextEventAt is the earliest pending retry time (zero/false when none). The
	// scheduler uses it so a nacked or recovered event always schedules its own
	// wake, even when no watcher is due.
	NextEventAt(sessionID string) (time.Time, bool, error)
	SetNextWake(name string, at *time.Time) error
}

// ServiceInfoBackend exposes a service's stored manifest JSON. The runner uses it
// for the standing goal and, when the backend cannot persist watcher state, for
// the manifest's intervals.
type ServiceInfoBackend interface {
	ServiceSpec(name string) (string, error)
}

// ServiceStateBackend persists a resident session's runtime state and bounded
// status document. It is separate from ServiceInfoBackend so a backend can
// expose a manifest without accepting state writes.
type ServiceStateBackend interface {
	// SetServiceState records state and status. It must not bump updated_at: a
	// service waking on a timer should not reorder the session list.
	SetServiceState(name, state, status string) error
}

// ServiceWatcherBackend is the durable half of host-side probes. The runner
// executes a probe when it is due, persists what it observed, and wakes the
// model only when that observation crossed the condition. A backend without it
// still runs a service: its watchers are then only a scheduling interval from
// the manifest and are never executed.
type ServiceWatcherBackend interface {
	// Watchers returns every probe attached to the session, by immutable id.
	Watchers(sessionID string) ([]WatcherState, error)
	// PutWatcher inserts or replaces one probe by (session id, name).
	PutWatcher(w WatcherState) error
	// SetWatcherState records one probe run. id is the stored watcher id.
	SetWatcherState(id string, lastRun, nextRun *time.Time, lastState, lastErr string) error
}

// WatcherState mirrors one persisted probe. Spec is config.MarshalWatcherSpec's
// JSON; the runner parses it only through config, so it needs no session types.
type WatcherState struct {
	ID         string     `json:"id,omitempty"`
	SessionID  string     `json:"session_id,omitempty"`
	Name       string     `json:"name"`
	Spec       string     `json:"spec,omitempty"`
	Enabled    bool       `json:"enabled"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`
	LastRun    *time.Time `json:"last_run,omitempty"`
	NextRun    *time.Time `json:"next_run,omitempty"`
	LastState  string     `json:"last_state,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
}

// QueuedEvent is one pending input, for inspection. The scheduler only delivers
// due events; this projection also shows work waiting for a retry or lease.
// Payload is bounded by the store; adapters decide how much to render.
type QueuedEvent struct {
	ID       int64     `json:"id"`
	Class    string    `json:"class"`
	Priority int       `json:"priority"`
	Kind     string    `json:"kind"`
	Source   string    `json:"source"`
	Payload  string    `json:"payload,omitempty"`
	At       time.Time `json:"at"`
	Deadline time.Time `json:"deadline"`
	Attempts int       `json:"attempts,omitempty"`
	// Trace names the chain of cross-session work this event belongs to, and Hops
	// how many deliveries led here. Both are host-assigned, so an operator can
	// follow one instruction across sessions without trusting the model's account.
	Trace string `json:"trace,omitempty"`
	Hops  int    `json:"hops,omitempty"`
	// State is pending, leased, or dead. Dead entries stay visible for inspection
	// instead of disappearing with the pending queue.
	State string `json:"state,omitempty"`
}

// ServiceView is the projection a client needs to inspect one resident session:
// durable metadata, the last status document, the manifest, the probes, and the
// queue. Phase and Error are filled from a live worker when one exists.
type ServiceView struct {
	Meta
	Status       string         `json:"status,omitempty"`
	Spec         string         `json:"spec,omitempty"`
	ManifestTOML string         `json:"manifest_toml,omitempty"`
	Phase        string         `json:"phase,omitempty"`
	Error        string         `json:"error,omitempty"`
	Watchers     []WatcherState `json:"watchers,omitempty"`
	Queue        []QueuedEvent  `json:"queue,omitempty"`
}

// ServiceBackend reads one service without requiring a live runtime. A backend
// without it cannot serve service inspection, but the CLI can still read its own
// store directly.
type ServiceBackend interface {
	ServiceView(name string) (ServiceView, error)
}

// ServiceCreator creates a resident session from a manifest. A backend without
// it cannot add services to a running server; the CLI writes them directly.
type ServiceCreator interface {
	// CreateService validates and stores the manifest, then returns the created
	// view. approveCommands pre-approves its command watchers, the HTTP
	// equivalent of `--yes`; without it they are stored unapproved and wait for
	// a grant.
	CreateService(name, manifest string, approveCommands bool) (ServiceView, error)
}

// GroupBackend is the optional group directory. A backend without it runs as if
// no session belonged to a group: membership is unrestricted, which is exactly
// the behaviour before groups existed.
type GroupBackend interface {
	GroupByName(name string) (core.Group, bool, error)
	GroupBySession(sessionID string) (core.Group, bool, error)
	GroupMembers(groupID string) ([]core.GroupMember, error)
	Groups() ([]core.Group, error)
}

// GroupAdminBackend is the write side of the directory. It takes session names
// because that is what an operator types or picks; the adapter resolves them
// against the store, which is also where "a member is a resident service" can be
// enforced. It is separate from GroupBackend so a backend that only reads groups
// (an older build, a test double) keeps working unchanged.
type GroupAdminBackend interface {
	// CreateGroup writes the directory rows. The lead, when set, must be one of the
	// members, and the member count may not exceed maxMembers.
	CreateGroup(name, goal, lead string, members []string, maxMembers int) error
	// AddGroupMember adds one existing service session. A session already in the
	// same group is a no-op; one in another group is refused.
	AddGroupMember(group, session string) error
	// RemoveGroupMember removes one membership. The lead cannot be removed.
	RemoveGroupMember(group, session string) error
	// DeleteGroup removes the directory rows and the board; member sessions stay.
	DeleteGroup(name string) error
}

// TaskBackend is the optional group board. A backend without it runs groups
// without a board: the task tools report that this host has none, and delivery
// and prompts are unaffected.
type TaskBackend interface {
	CreateTask(core.Task) (core.Task, error)
	Task(id string) (core.Task, error)
	Tasks(groupID string, statuses []core.TaskStatus, limit int) ([]core.Task, error)
	ClaimTask(id, ownerID string, now time.Time, lease time.Duration) (core.Task, error)
	UpdateTask(id, actorID string, expectVersion int, status core.TaskStatus, note string, allowForeign bool, now time.Time) (core.Task, error)
	ExpireTasks(now time.Time) ([]core.Task, error)
	OrphanTasksOwnedBy(sessionID string, now time.Time) ([]core.Task, error)
	DueEscalations(groupID string, now time.Time) ([]core.Task, error)
	MarkTaskEscalated(id string, kind core.TaskStatus, now time.Time) (bool, error)
}

// ServiceEditor replaces a stopped service's manifest. It is deliberately not a
// live edit: the session identity, history, and queued events are kept, while the
// role, workspace, spec, and watcher set are reconciled.
type ServiceEditor interface {
	EditService(name, manifest string, approveCommands bool) (ServiceView, error)
}

// WorkspaceBackend resolves the immutable workspace when preparing a runtime.
// The returned Meta carries the session's working directory and its creation time:
// the runtime pins that birth time into the prompt head, so a fresh runtime
// reconstructs the same bytes a retired one sent and the provider's prefix cache
// still matches. Resource ownership follows Backend.Prepare, including on failure.
type WorkspaceBackend interface {
	PrepareWorkspace(name, cwd string) (*config.UserConfig, provider.Provider, core.Recorder, []provider.Message, func(), Meta, error)
}

type CreationBackend interface {
	Create(name, cwd string) (Meta, error)
}

// RoleBackend exposes role definitions without coupling transports to prompts.
type RoleBackend interface {
	Roles() ([]string, string, error)
	CreateWithRole(name, cwd, role string) (Meta, error)
}

// ModelBackend is optional; model discovery must not require remote API calls.
type ModelBackend interface {
	Models() ([]string, string, error)
	SetModel(name, model string) error
}

// ReasoningBackend is optional; it reports the configured default reasoning
// effort (from config.toml), used when a session has no in-memory override.
type ReasoningBackend interface {
	ReasoningDefault() (string, error)
}

// ContextBackend is optional; it reports the context a session would send, for
// inspection. offset and limit window the cards exactly like History does, so a
// long transcript is never returned whole. It reads persisted state only, so it
// must not require a live runtime.
type ContextBackend interface {
	Context(name string, offset, limit int) (ContextView, error)
}

// ContextSetter is optional; it persists the user's surgery on one turn's place
// in the context-to-send. action is core.OverrideReplace or core.OverrideDrop;
// DeleteOverride clears it. SetEssential is not an override: it flags the stored
// turn so replay never elides it and a summary must preserve its content. Like
// the other setters they must not require a live runtime, but the manager retires
// any idle one so the next instruction re-prepares with the new projection.
type ContextSetter interface {
	SetOverride(name string, seq int, action, content string) error
	DeleteOverride(name string, seq int) error
	SetEssential(name string, seq int, on bool) error
}

// RewindBackend is optional; truncating a session's persisted history must not
// require a backend that cannot persist. backup requests a snapshot of the
// dropped turns (nil lets the backend choose its configured default).
type RewindBackend interface {
	Rewind(name string, keep int, backup *bool) error
}

// RoleSetter is optional; changing a session's role must not require a backend
// that cannot persist roles.
type RoleSetter interface {
	SetRole(name, role string) error
}

// WorkingDirSetter is optional; changing a workspace requires a backend that
// can persist it independently of the runtime.
type WorkingDirSetter interface {
	SetWorkingDir(name, cwd string) error
}

// RenameBackend is optional; renaming a session must not require a backend that
// cannot persist.
type RenameBackend interface {
	Rename(name, newName string) error
}

// DeleteBackend is optional; deleting a session must not require a backend that
// cannot persist.
type DeleteBackend interface {
	Delete(name string) error
}

type Prompt struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Title     string         `json:"title"`
	Tool      *core.ToolCall `json:"tool,omitempty"`
	Untrusted []string       `json:"untrusted,omitempty"`
	// TrustTarget says what Untrusted names are: "command" for command names,
	// "path" for a directory, or "tool" for custom tool names, so a client can
	// word the approval correctly.
	TrustTarget string `json:"trust_target,omitempty"`
	// Persist reports whether an "always" grant may be stored. Project tools set
	// it false: they are session-only.
	Persist bool `json:"persist"`
}

func clonePrompt(p *Prompt) *Prompt {
	if p == nil {
		return nil
	}
	copy := *p
	copy.Tool = cloneToolCall(p.Tool)
	copy.Untrusted = append([]string(nil), p.Untrusted...)
	return &copy
}

// cloneToolCall deep-copies a ToolCall's slices, so every subscriber or snapshot
// gets an independent value (mutating one never leaks into another).
func cloneToolCall(c *core.ToolCall) *core.ToolCall {
	if c == nil {
		return nil
	}
	dup := *c
	dup.CommandSegments = append([]string(nil), c.CommandSegments...)
	dup.UntrustedSegments = append([]int(nil), c.UntrustedSegments...)
	return &dup
}

type Snapshot struct {
	Usage      core.Usage `json:"usage"`
	TotalUsage core.Usage `json:"total_usage"`
	UsageCalls int        `json:"usage_calls"`
	// ContextTokens is the configured compaction threshold: the last prompt size
	// at which older turns get summarized. 0 means compaction is disabled, so a
	// client should not render a context budget.
	ContextTokens int `json:"context_tokens,omitempty"`
	// TrustedCmds is the per-session grant of command names approved for this
	// conversation. The manager holds it, so it survives the runtime being retired;
	// a client renders it as the state of its own trust switches.
	TrustedCmds []string `json:"trusted_cmds,omitempty"`
	// TrustedTools is the custom-tool counterpart, keyed by tool name.
	TrustedTools []string       `json:"trusted_tools,omitempty"`
	Name         string         `json:"name"`
	Model        string         `json:"model"`
	State        string         `json:"state"` // starting, busy, awaiting, suspended, parked, stopped
	Phase        string         `json:"phase"`
	ActiveTool   *core.ToolCall `json:"active_tool,omitempty"`
	Queued       int            `json:"queued"`
	Pending      *Prompt        `json:"pending,omitempty"`
	Error        string         `json:"error,omitempty"`
	Reasoning    string         `json:"reasoning,omitempty"`
	// TrustAll reports that every confirmation for this session is auto-approved
	// without a prompt. It is a per-session, in-memory toggle the user sets
	// explicitly; it is never persisted to config.
	TrustAll bool `json:"trust_all,omitempty"`
}

func cloneSnapshot(s Snapshot) Snapshot {
	s.ActiveTool = cloneToolCall(s.ActiveTool)
	s.Pending = clonePrompt(s.Pending)
	return s
}

// ResolveWorkingDir validates an existing directory without changing process cwd.
func ResolveWorkingDir(cwd string) (string, error) {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("stat working directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("working directory is not a directory")
	}
	return dir, nil
}
