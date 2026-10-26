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
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/paths"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/session"
	"github.com/Carudy/pai/internal/tui"
)

func serviceHelp() string {
	return `SUBCOMMANDS
  list, ls                       List service sessions (mode = service)
  show, cat <name>               Show the manifest, status, watchers, and next wake
  create, new <name> --file F    Create a service from a manifest [--yes]

A service is a resident session. ` + "`pai serve`" + ` starts the scheduler and registers
every service it finds; this command only creates and inspects the durable state.
Creating a manifest with command watchers requires --yes: a command probe is
persistent code execution on a schedule, so review it first.

The session store is exclusive, so stop ` + "`pai serve`" + ` before running create/show/list.`
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
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, serviceHelp())
		return 0
	default:
		log.Errorf("Unknown service command %q; try: list | show | create\n", args[0])
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
	roles := prompts.RoleNames()
	knownRole := false
	for _, role := range roles {
		if role == manifest.Role {
			knownRole = true
			break
		}
	}
	if !knownRole {
		log.Errorf("Error: unknown role %q; available: %s\n", manifest.Role, strings.Join(roles, ", "))
		return 1
	}
	workspace, err := resolveServiceWorkspace(manifest.Workspace)
	if err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	var commands []string
	for _, w := range manifest.Watchers {
		if w.Kind == "command" {
			commands = append(commands, fmt.Sprintf("%s: %s", w.Name, w.Cmd))
		}
	}
	if len(commands) > 0 && !yes {
		log.Errorf("Error: this manifest defines command watchers, which run persistently on a schedule:\n")
		for _, c := range commands {
			log.Errorf("  %s\n", c)
		}
		log.Errorf("Re-run with --yes after reviewing them.\n")
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
	created, err := store.Create(session.Meta{
		Name: name, Role: manifest.Role, Model: cfg.DefaultModel,
		Cwd: workspace, Mode: session.ModeService,
	})
	if err != nil {
		log.Errorf("Error creating service: %v\n", err)
		return 1
	}
	ss, ok := store.(session.ServiceStore)
	if !ok {
		log.Errorf("Error: this session store does not expose service state\n")
		return 1
	}
	if err := ss.SetServiceSpec(name, manifest.JSON); err != nil {
		log.Errorf("Error storing manifest: %v\n", err)
		return 1
	}
	now := time.Now()
	for _, w := range manifest.Watchers {
		next := now.Add(w.Every)
		if err := ss.PutWatcher(session.Watcher{
			SessionID: created.Meta.ID, Name: w.Name, Spec: config.MarshalWatcherSpec(w),
			Enabled: true, ApprovedAt: &now, NextRun: &next,
		}); err != nil {
			log.Errorf("Error storing watcher %q: %v\n", w.Name, err)
			return 1
		}
	}
	next := "-"
	if at, ok := manifest.NextWake(now); ok {
		if err := ss.SetNextWake(name, &at); err != nil {
			log.Errorf("Error storing the next wake: %v\n", err)
			return 1
		}
		next = at.Format(time.RFC3339)
	}
	fmt.Fprintf(stdout, "created service %s (id %s)\n", name, created.Meta.ID)
	fmt.Fprintf(stdout, "workspace: %s\nrole: %s\nnext wake: %s\n", workspace, manifest.Role, next)
	fmt.Fprintln(stdout, "run `pai serve` to start the scheduler")
	return 0
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
