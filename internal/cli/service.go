package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/paths"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/runner"
	"github.com/Carudy/pai/internal/session"
	"github.com/Carudy/pai/internal/tui"
)

func serviceHelp() string {
	return `SUBCOMMANDS
  list, ls                       List service sessions (mode = service)
  show, cat <name>               Show the manifest, status, watchers, and next wake
  create, new <name> --file F    Create a service from a manifest [--yes]
  edit, update <name> --file F   Replace a stopped service's manifest [--yes]
  rm, delete <name>              Delete the service, its history, and its watchers
  stop, disable <name>           Stop scheduling; the stopped state survives restarts
  start, enable <name>           Resume a stopped service and probe immediately

A service is a resident session. ` + "`pai serve`" + ` starts the scheduler and registers
every service it finds; this command only creates, inspects, and edits the durable
state. A manifest that defines command watchers is saved as written: --yes
pre-approves them, and otherwise the first run asks for confirmation with the same
card a chat tool call uses (once / trust session / always). A command name already
trusted for the session counts as a grant.
A manifest edit is only allowed while the service is stopped; the role, workspace,
spec, and watcher set are replaced, while the session's history and queued events
are kept. A periodic report is just a timer watcher. Deleting is the one irreversible action: it removes the
service's transcript, queued events, and watcher grants as well as the row, so a
recreate starts clean. A chat session is refused here; use ` + "`pai session rm`" + `.

The session store is exclusive, so stop ` + "`pai serve`" + ` before running create/show/list.
While a server is running, GET /api/service?name=<name> returns the same view.`
}

func runServiceCmd(_ context.Context, args []string, stdout io.Writer, log *tui.Logger) int {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list", "ls":
		return serviceList(stdout, log)
	case "show", "cat":
		if len(args) < 2 {
			log.Errorf("Usage: pai service show <name>\n")
			return 1
		}
		return serviceShow(args[1], stdout, log)
	case "create", "new":
		return serviceCreate(args[1:], stdout, log)
	case "edit", "update":
		return serviceEdit(args[1:], stdout, log)
	case "rm", "delete", "del":
		if len(args) < 2 {
			log.Errorf("Usage: pai service rm <name>\n")
			return 1
		}
		return serviceDelete(args[1], stdout, log)
	case "stop", "disable":
		if len(args) < 2 {
			log.Errorf("Usage: pai service stop <name>\n")
			return 1
		}
		return serviceSetState(args[1], runner.StateStopped, stdout, log)
	case "start", "enable":
		if len(args) < 2 {
			log.Errorf("Usage: pai service start <name>\n")
			return 1
		}
		return serviceSetState(args[1], runner.StateSuspended, stdout, log)
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, serviceHelp())
		return 0
	default:
		log.Errorf("Unknown service command %q; try: list | show | create | edit | rm | stop | start\n", args[0])
		return 1
	}
}

func openServiceStore(log *tui.Logger) (session.Store, bool) {
	store, err := session.Open()
	if err != nil {
		log.Errorf("Error opening sessions: %v\n", err)
		return nil, false
	}
	return store, true
}

// serviceDelete removes a service and everything attached to it. The store is
// exclusive, so no scheduler is running; watchers, queued events, and turns
// follow the row through ON DELETE CASCADE. A chat session is refused: deleting
// the wrong kind by a typo should not be easy.
func serviceDelete(name string, stdout io.Writer, log *tui.Logger) int {
	store, ok := openServiceStore(log)
	if !ok {
		return 1
	}
	defer store.Close()
	ss, ok := store.(session.ServiceStore)
	if !ok {
		log.Errorf("Error: this session store does not expose service state\n")
		return 1
	}
	info, err := ss.ServiceInfo(name)
	if err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	if info.Mode != session.ModeService {
		log.Errorf("Error: %q is not a service; use `pai session rm %s`\n", name, name)
		return 1
	}
	if err := store.Delete(name); err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "deleted service %s\n", name)
	return 0
}

func serviceList(stdout io.Writer, log *tui.Logger) int {
	store, ok := openServiceStore(log)
	if !ok {
		return 1
	}
	defer store.Close()
	metas, err := store.List()
	if err != nil {
		log.Errorf("Error listing sessions: %v\n", err)
		return 1
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tNEXT WAKE\tEVENTS\tROLE\tGOAL")
	found := false
	for _, meta := range metas {
		if meta.Mode != session.ModeService {
			continue
		}
		found = true
		events := 0
		goal := ""
		if ss, ok := store.(session.ServiceStore); ok {
			events, _ = ss.PendingEvents(meta.ID)
			if info, err := ss.ServiceInfo(meta.Name); err == nil {
				if manifest, err := config.ParseStoredServiceSpec(info.Spec); err == nil {
					goal = manifest.Goal
				}
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n",
			meta.Name, emptyOr(meta.State, "-"), formatWake(meta.NextWake), events, meta.Role, goal)
	}
	if err := tw.Flush(); err != nil {
		log.Errorf("Error writing list: %v\n", err)
		return 1
	}
	if !found {
		fmt.Fprintln(stdout, "no service sessions")
	}
	return 0
}

func serviceShow(name string, stdout io.Writer, log *tui.Logger) int {
	store, ok := openServiceStore(log)
	if !ok {
		return 1
	}
	defer store.Close()

	ss, ok := store.(session.ServiceStore)
	if !ok {
		log.Errorf("Error: this session store does not expose service state\n")
		return 1
	}
	info, err := ss.ServiceInfo(name)
	if err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	events, _ := ss.PendingEvents(info.ID)
	fmt.Fprintf(stdout, "name:        %s\n", info.Name)
	fmt.Fprintf(stdout, "id:          %s\n", info.ID)
	fmt.Fprintf(stdout, "role:        %s\n", info.Role)
	fmt.Fprintf(stdout, "model:       %s\n", info.Model)
	fmt.Fprintf(stdout, "workspace:   %s\n", info.Cwd)
	fmt.Fprintf(stdout, "state:       %s\n", emptyOr(info.State, "-"))
	fmt.Fprintf(stdout, "next wake:   %s\n", formatWake(info.NextWake))
	fmt.Fprintf(stdout, "events:      %d pending\n", events)
	if queued, err := ss.QueuedEvents(info.ID, 20); err == nil && len(queued) > 0 {
		fmt.Fprintln(stdout, "queue:")
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  CLASS\tPRIORITY\tSOURCE\tAT\tATTEMPTS\tDETAIL")
		for _, ev := range queued {
			fmt.Fprintf(tw, "  %s\t%d\t%s\t%s\t%d\t%s\n",
				ev.Class, ev.Priority, ev.Source, formatWake(&ev.At), ev.Attempts,
				emptyOr(clip(strings.TrimSpace(ev.Payload), 60), "-"))
		}
		if err := tw.Flush(); err != nil {
			log.Errorf("Error writing queue: %v\n", err)
			return 1
		}
	}
	if strings.TrimSpace(info.Status) != "" {
		fmt.Fprintf(stdout, "status:\n%s\n", strings.TrimSpace(info.Status))
	}

	if info.Spec != "" && info.Spec != "{}" {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, []byte(info.Spec), "", "  "); err == nil {
			fmt.Fprintf(stdout, "manifest:\n%s\n", pretty.String())
		} else {
			fmt.Fprintf(stdout, "manifest: %s\n", info.Spec)
		}
	}

	watchers, err := ss.Watchers(info.ID)
	if err != nil {
		log.Errorf("Error reading watchers: %v\n", err)
		return 1
	}
	if len(watchers) == 0 {
		fmt.Fprintln(stdout, "watchers:    none")
		return 0
	}
	fmt.Fprintln(stdout, "watchers:")
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  NAME\tENABLED\tLAST RUN\tNEXT RUN\tSTATE\tERROR")
	for _, w := range watchers {
		fmt.Fprintf(tw, "  %s\t%v\t%s\t%s\t%s\t%s\n",
			w.Name, w.Enabled, formatWake(w.LastRun), formatWake(w.NextRun), emptyOr(w.LastState, "-"), w.LastError)
	}
	if err := tw.Flush(); err != nil {
		log.Errorf("Error writing watchers: %v\n", err)
		return 1
	}
	return 0
}

// serviceSetState stops or resumes a service in the durable store. It requires
// the store lock, so it only works while `pai serve` is stopped; a running server
// exposes the same transition through /api/service/stop and /api/service/start.
func serviceSetState(name, state string, stdout io.Writer, log *tui.Logger) int {
	store, ok := openServiceStore(log)
	if !ok {
		return 1
	}
	defer store.Close()
	ss, ok := store.(session.ServiceStore)
	if !ok {
		log.Errorf("Error: this session store does not expose service state\n")
		return 1
	}
	info, err := ss.ServiceInfo(name)
	if err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	if info.Mode != session.ModeService {
		log.Errorf("Error: %q is not a service\n", name)
		return 1
	}
	switch state {
	case runner.StateStopped:
		if err := ss.SetServiceState(name, runner.StateStopped, "stopped by user"); err != nil {
			log.Errorf("Error stopping service: %v\n", err)
			return 1
		}
		if err := ss.SetNextWake(name, nil); err != nil {
			log.Errorf("Error clearing next wake: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "stopped service %s; start it with `pai service start %s`\n", name, name)
	case runner.StateSuspended:
		now := time.Now()
		if err := ss.SetServiceState(name, runner.StateSuspended, ""); err != nil {
			log.Errorf("Error starting service: %v\n", err)
			return 1
		}
		if err := ss.SetNextWake(name, &now); err != nil {
			log.Errorf("Error scheduling the first wake: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "started service %s; it will wake on the next server tick\n", name)
	default:
		log.Errorf("Error: unsupported state %q\n", state)
		return 1
	}
	return 0
}

func serviceCreate(args []string, stdout io.Writer, log *tui.Logger) int {
	if len(args) == 0 {
		log.Errorf("Usage: pai service create <name> --file <manifest.toml> [--yes]\n")
		return 1
	}
	name := args[0]
	var file string
	yes := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--file", "-f":
			if i+1 >= len(args) {
				log.Errorf("Error: %s needs a path\n", args[i])
				return 1
			}
			file = args[i+1]
			i++
		case "--yes", "-y":
			yes = true
		default:
			log.Errorf("Unknown argument %q; usage: pai service create <name> --file <manifest.toml> [--yes]\n", args[i])
			return 1
		}
	}
	if file == "" {
		log.Errorf("Usage: pai service create <name> --file <manifest.toml> [--yes]\n")
		return 1
	}
	if !session.ValidName(name) {
		log.Errorf("Error: invalid session name %q (allowed: letters, digits, . _ -)\n", name)
		return 1
	}
	data, err := os.ReadFile(file)
	if err != nil {
		log.Errorf("Error reading manifest: %v\n", err)
		return 1
	}
	manifest, err := config.ParseServiceManifest(data)
	if err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	if err := prepareServiceManifest(manifest); err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	cfg, err := config.LoadUserConfig()
	if err != nil {
		log.Errorf("Error loading config: %v\n", err)
		return 1
	}
	if cfg.DefaultModel == "" {
		log.Errorf("Error: no default model is configured\n")
		return 1
	}

	store, ok := openServiceStore(log)
	if !ok {
		return 1
	}
	defer store.Close()
	if _, err := store.Get(name); err == nil {
		log.Errorf("Error: session %q already exists\n", name)
		return 1
	} else if !errors.Is(err, session.ErrNotFound) {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	created, next, err := newService(store, name, manifest, cfg.DefaultModel, time.Now(), yes)
	if err != nil {
		log.Errorf("Error creating service: %v\n", err)
		return 1
	}
	when := "-"
	if next != nil {
		when = next.Format(time.RFC3339)
	}
	fmt.Fprintf(stdout, "created service %s (id %s)\n", name, created.ID)
	fmt.Fprintf(stdout, "workspace: %s\nrole: %s\nnext wake: %s\n", manifest.Workspace, manifest.Role, when)
	printConfirmationHint(stdout, manifest, yes)
	fmt.Fprintln(stdout, "run `pai serve` to start the scheduler")
	return 0
}

// serviceEdit replaces a stopped service's manifest. It shares validation and
// command approval with create; the store stays the durable authority, so a
// running server must be stopped first, like every other service CLI write.
func serviceEdit(args []string, stdout io.Writer, log *tui.Logger) int {
	if len(args) == 0 {
		log.Errorf("Usage: pai service edit <name> --file <manifest.toml> [--yes]\n")
		return 1
	}
	name := args[0]
	var file string
	yes := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--file", "-f":
			if i+1 >= len(args) {
				log.Errorf("Error: %s needs a path\n", args[i])
				return 1
			}
			file = args[i+1]
			i++
		case "--yes", "-y":
			yes = true
		default:
			log.Errorf("Unknown argument %q; usage: pai service edit <name> --file <manifest.toml> [--yes]\n", args[i])
			return 1
		}
	}
	if file == "" {
		log.Errorf("Usage: pai service edit <name> --file <manifest.toml> [--yes]\n")
		return 1
	}
	if !session.ValidName(name) {
		log.Errorf("Error: invalid session name %q (allowed: letters, digits, . _ -)\n", name)
		return 1
	}
	data, err := os.ReadFile(file)
	if err != nil {
		log.Errorf("Error reading manifest: %v\n", err)
		return 1
	}
	manifest, err := config.ParseServiceManifest(data)
	if err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	if err := prepareServiceManifest(manifest); err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	store, ok := openServiceStore(log)
	if !ok {
		return 1
	}
	defer store.Close()
	if err := updateService(store, name, manifest, time.Now(), yes); err != nil {
		log.Errorf("Error updating service: %v\n", err)
		return 1
	}
	printConfirmationHint(stdout, manifest, yes)
	fmt.Fprintf(stdout, "updated service %s; start it to apply the new manifest\n", name)
	return 0
}

// prepareServiceManifest applies the checks both entry points share: the role
// must exist and the workspace must be a real directory. It resolves the
// workspace in place, so the stored manifest is canonical.
func prepareServiceManifest(manifest *config.ServiceManifest) error {
	roles := prompts.RoleNames()
	if !slices.Contains(roles, manifest.Role) {
		return fmt.Errorf("unknown role %q; available: %s", manifest.Role, strings.Join(roles, ", "))
	}
	workspace, err := resolveServiceWorkspace(manifest.Workspace)
	if err != nil {
		return err
	}
	manifest.Workspace = workspace
	return nil
}

// commandWatchers names the probes that execute code on a schedule. Without
// pre-approval their first run asks for confirmation, so the entry point says so
// instead of letting a correctly stored service look idle.
func commandWatchers(manifest *config.ServiceManifest) []string {
	var out []string
	for _, w := range manifest.Watchers {
		if w.Command != "" {
			out = append(out, fmt.Sprintf("%s: %s", w.Name, w.Command))
		}
	}
	return out
}

func printConfirmationHint(stdout io.Writer, manifest *config.ServiceManifest, approveCommands bool) {
	if approveCommands {
		return
	}
	commands := commandWatchers(manifest)
	if len(commands) == 0 {
		return
	}
	fmt.Fprintln(stdout, "command watchers are not pre-approved; their first run asks for confirmation:")
	for _, c := range commands {
		fmt.Fprintln(stdout, "  "+c)
	}
	fmt.Fprintln(stdout, "answer it in the web UI or `pai conn` (once / trust session / always)")
}

// newService writes the durable half of a resident session: the row, its
// manifest, one watcher per probe (plus the reserved report tick), and the
// initial next wake. approveCommands is the entry point's "pre-approve now";
// without it the first probe asks for confirmation.
func newService(store session.Store, name string, manifest *config.ServiceManifest, model string, now time.Time, approveCommands bool) (session.Meta, *time.Time, error) {
	ss, ok := store.(session.ServiceStore)
	if !ok {
		return session.Meta{}, nil, errors.New("this session store does not expose service state")
	}
	created, err := store.Create(session.Meta{
		Name: name, Role: manifest.Role, Model: model, Cwd: manifest.Workspace, Mode: session.ModeService,
	})
	if err != nil {
		return session.Meta{}, nil, err
	}
	if err := ss.SetServiceSpec(name, manifest.JSON); err != nil {
		return session.Meta{}, nil, fmt.Errorf("store manifest: %w", err)
	}
	if err := reconcileWatchers(ss, created.Meta.ID, manifest, now, nil, false, approveCommands); err != nil {
		return session.Meta{}, nil, err
	}
	var next *time.Time
	if at, ok := manifest.NextWake(now); ok {
		if err := ss.SetNextWake(name, &at); err != nil {
			return session.Meta{}, nil, fmt.Errorf("store next wake: %w", err)
		}
		next = &at
	}
	return created.Meta, next, nil
}

// reconcileWatchers makes the stored watcher set match a manifest: unchanged
// specs keep their observation state and their grant, changed or new ones start
// clean, and rows no longer in the manifest are removed. dueNow is for an edit:
// the service is stopped, so the next run is left unset and the first start
// probes at once.
//
// A command probe is pre-approved only by approveCommands or by an existing
// pre-approval on an unchanged spec. A changed command needs a new decision: the
// old one was about code that no longer runs. Without a pre-approval the first
// run asks through the ordinary confirmation card.
func reconcileWatchers(ss session.ServiceStore, sessionID string, manifest *config.ServiceManifest, now time.Time, existing []session.Watcher, dueNow, approveCommands bool) error {
	specs := append([]config.WatcherSpec(nil), manifest.Watchers...)
	byName := make(map[string]session.Watcher, len(existing))
	for _, w := range existing {
		byName[w.Name] = w
	}
	keep := make(map[string]bool, len(specs))
	for _, spec := range specs {
		keep[spec.Name] = true
		encoded := config.MarshalWatcherSpec(spec)
		w := session.Watcher{
			SessionID: sessionID, Name: spec.Name, Spec: encoded, Enabled: true,
		}
		// A timer runs no code, so there is nothing to approve.
		if spec.Command == "" || approveCommands {
			w.ApprovedAt = &now
		}
		if old, ok := byName[spec.Name]; ok {
			if old.Spec == encoded {
				// An unchanged probe keeps what it last observed and its grant.
				w.LastState, w.LastError = old.LastState, old.LastError
				if old.ApprovedAt != nil {
					w.ApprovedAt = old.ApprovedAt
				}
			}
		}
		if !dueNow {
			next := now.Add(spec.Interval)
			w.NextRun = &next
		}
		if err := ss.PutWatcher(w); err != nil {
			return fmt.Errorf("store watcher %q: %w", spec.Name, err)
		}
	}
	for _, old := range existing {
		if keep[old.Name] {
			continue
		}
		if err := ss.DeleteWatcher(old.ID); err != nil {
			return fmt.Errorf("remove watcher %q: %w", old.Name, err)
		}
	}
	return nil
}

// updateService replaces a stopped service's manifest. The session identity,
// history, and queued events stay; role, workspace, spec, and the watcher set
// are reconciled. Editing a live service would race its next activation, so it
// is refused rather than silently deferred.
func updateService(store session.Store, name string, manifest *config.ServiceManifest, now time.Time, approveCommands bool) error {
	ss, ok := store.(session.ServiceStore)
	if !ok {
		return errors.New("this session store does not expose service state")
	}
	info, err := ss.ServiceInfo(name)
	if err != nil {
		return err
	}
	if info.Mode != session.ModeService {
		return fmt.Errorf("%q is not a service", name)
	}
	// A service that has never run has no state yet; it is as idle as a stopped
	// one, so an immediate edit after creation is allowed.
	if info.State != runner.StateStopped && info.State != "" {
		return errors.New("stop the service before editing its manifest")
	}
	if err := store.SetRole(name, manifest.Role); err != nil {
		return fmt.Errorf("store role: %w", err)
	}
	if err := store.SetWorkingDir(name, manifest.Workspace); err != nil {
		return fmt.Errorf("store workspace: %w", err)
	}
	if err := ss.SetServiceSpec(name, manifest.JSON); err != nil {
		return fmt.Errorf("store manifest: %w", err)
	}
	watchers, err := ss.Watchers(info.ID)
	if err != nil {
		return err
	}
	if err := reconcileWatchers(ss, info.ID, manifest, now, watchers, true, approveCommands); err != nil {
		return err
	}
	if err := ss.SetNextWake(name, nil); err != nil {
		return fmt.Errorf("clear next wake: %w", err)
	}
	return nil
}

func resolveServiceWorkspace(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("service.workspace is required")
	}
	if path == "~" {
		path = paths.Home()
	} else if strings.HasPrefix(path, "~/") {
		path = filepath.Join(paths.Home(), path[2:])
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("workspace %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace %s is not a directory", abs)
	}
	return abs, nil
}

func formatWake(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.Format(time.RFC3339)
}

func emptyOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
