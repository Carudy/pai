package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/runner"
	"github.com/Carudy/pai/internal/session"
	"github.com/Carudy/pai/internal/tui"
)

func groupHelp() string {
	return `SUBCOMMANDS
  list, ls                     List groups with their lead and member count
  show, cat <name>             Show the goal, members, and state
  create, new --file F         Create a group from a manifest
  add <group> <session>        Add an existing service session to a group
  rm-member <group> <session>  Remove a member (the lead cannot be removed)
  start, enable <group>        Resume every member service
  stop, disable <group>        Stop every member service
  rm, delete <name>            Delete the group directory (sessions are kept)

A group is a membership boundary over existing service sessions, not a new kind
of process: each member keeps its own role, model, workspace, and watchers. It
decides who may mail whom, and it gives every member the same three things: a
shared goal, the roster, and (if their role lacks it) the ` + "`mail`" + ` tool. It
schedules nothing and budgets nothing.

` + "`group:<name>`" + ` broadcasts to every other member, so a group address is one
message and N copies, not N². ` + "`lead`" + ` is optional and is only an escalation
hint — it plays no role in addressing.

The store is exclusive, so stop ` + "`pai serve`" + ` before creating, editing, or removing a
group; a running server enforces the membership it read at startup, so a change
takes effect when the server restarts.
`
}

func runGroupCmd(_ context.Context, args []string, stdout io.Writer, log *tui.Logger) int {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list", "ls":
		return groupList(stdout, log)
	case "show", "cat":
		if len(args) < 2 {
			log.Errorf("Usage: pai group show <name>\n")
			return 1
		}
		return groupShow(args[1], stdout, log)
	case "create", "new":
		return groupCreate(args[1:], stdout, log)
	case "add":
		if len(args) < 3 {
			log.Errorf("Usage: pai group add <group> <session>\n")
			return 1
		}
		return groupAdd(args[1], args[2], stdout, log)
	case "rm-member", "remove":
		if len(args) < 3 {
			log.Errorf("Usage: pai group rm-member <group> <session>\n")
			return 1
		}
		return groupRemoveMember(args[1], args[2], stdout, log)
	case "rm", "delete", "del":
		if len(args) < 2 {
			log.Errorf("Usage: pai group rm <name>\n")
			return 1
		}
		return groupDelete(args[1], stdout, log)
	case "start", "enable":
		if len(args) < 2 {
			log.Errorf("Usage: pai group start <name>\n")
			return 1
		}
		return groupSetState(args[1], runner.StateSuspended, stdout, log)
	case "stop", "disable":
		if len(args) < 2 {
			log.Errorf("Usage: pai group stop <name>\n")
			return 1
		}
		return groupSetState(args[1], runner.StateStopped, stdout, log)
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, groupHelp())
		return 0
	default:
		log.Errorf("Unknown group command %q; try: list | show | create | add | rm-member | start | stop | rm\n", args[0])
		return 1
	}
}

// openGroupStore opens the session store and exposes the group directory. The
// store stays open for the caller, which needs meta reads too.
func openGroupStore(log *tui.Logger) (session.Store, session.GroupStore, bool) {
	store, ok := openServiceStore(log)
	if !ok {
		return nil, nil, false
	}
	gs, ok := store.(session.GroupStore)
	if !ok {
		store.Close()
		log.Errorf("Error: this session store does not expose groups\n")
		return nil, nil, false
	}
	return store, gs, true
}

func groupByName(gs session.GroupStore, name string) (core.Group, bool, bool) {
	g, found, err := gs.GroupByName(name)
	if err != nil {
		return core.Group{}, false, false
	}
	return g, found, true
}

func groupList(stdout io.Writer, log *tui.Logger) int {
	store, gs, ok := openGroupStore(log)
	if !ok {
		return 1
	}
	defer store.Close()
	groups, err := gs.Groups()
	if err != nil {
		log.Errorf("Error listing groups: %v\n", err)
		return 1
	}
	if len(groups) == 0 {
		fmt.Fprintln(stdout, "no groups")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tLEAD\tMEMBERS")
	for _, g := range groups {
		members, err := gs.GroupMembers(g.ID)
		if err != nil {
			log.Errorf("Error reading members of %s: %v\n", g.Name, err)
			return 1
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\n", g.Name, emptyOr(g.LeadName, "-"), len(members))
	}
	if err := tw.Flush(); err != nil {
		log.Errorf("Error writing list: %v\n", err)
		return 1
	}
	return 0
}

func groupShow(name string, stdout io.Writer, log *tui.Logger) int {
	store, gs, ok := openGroupStore(log)
	if !ok {
		return 1
	}
	defer store.Close()
	g, found, ok := groupByName(gs, name)
	if !ok {
		log.Errorf("Error reading group %s\n", name)
		return 1
	}
	if !found {
		log.Errorf("Error: group %q not found\n", name)
		return 1
	}
	members, err := gs.GroupMembers(g.ID)
	if err != nil {
		log.Errorf("Error reading members: %v\n", err)
		return 1
	}
	metas, err := store.List()
	if err != nil {
		log.Errorf("Error listing sessions: %v\n", err)
		return 1
	}
	state := make(map[string]string, len(metas))
	for _, m := range metas {
		state[m.ID] = m.State
	}
	fmt.Fprintf(stdout, "name:        %s\n", g.Name)
	fmt.Fprintf(stdout, "id:          %s\n", g.ID)
	fmt.Fprintf(stdout, "lead:        %s\n", emptyOr(g.LeadName, "-"))
	fmt.Fprintf(stdout, "members:     %d/%d\n", len(members), g.MaxMembers)
	if goal := strings.TrimSpace(g.Goal); goal != "" {
		fmt.Fprintf(stdout, "goal:        %s\n", goal)
	}
	fmt.Fprintln(stdout, "member               state")
	for _, m := range members {
		fmt.Fprintf(stdout, "%-20s %s\n", m.Name, emptyOr(state[m.SessionID], "-"))
	}
	return 0
}

func groupCreate(args []string, stdout io.Writer, log *tui.Logger) int {
	var file string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--file", "-f":
			if i+1 >= len(args) {
				log.Errorf("Error: %s needs a path\n", args[i])
				return 1
			}
			file = args[i+1]
			i++
		default:
			log.Errorf("Unknown argument %q; usage: pai group create --file <manifest.toml>\n", args[i])
			return 1
		}
	}
	if file == "" {
		log.Errorf("Usage: pai group create --file <manifest.toml>\n")
		return 1
	}
	data, err := os.ReadFile(file)
	if err != nil {
		log.Errorf("Error reading %s: %v\n", file, err)
		return 1
	}
	manifest, err := config.ParseGroupManifest(data)
	if err != nil {
		log.Errorf("Error: %v\n", err)
		return 1
	}
	if !session.ValidName(manifest.Name) {
		log.Errorf("Error: invalid group name %q (allowed: letters, digits, . _ -)\n", manifest.Name)
		return 1
	}

	store, gs, ok := openGroupStore(log)
	if !ok {
		return 1
	}
	defer store.Close()

	metas, err := store.List()
	if err != nil {
		log.Errorf("Error listing sessions: %v\n", err)
		return 1
	}
	byName := make(map[string]session.Meta, len(metas))
	for _, m := range metas {
		byName[m.Name] = m
	}
	ids := make([]string, 0, len(manifest.Members))
	for _, name := range manifest.Members {
		meta, exists := byName[name]
		if !exists {
			log.Errorf("Error: %q is not a session; create it with `pai service create %s --file ...` first\n", name, name)
			return 1
		}
		if meta.Mode != session.ModeService {
			log.Errorf("Error: %q is a chat session; a group member must be a service session\n", name)
			return 1
		}
		ids = append(ids, meta.ID)
	}
	leadID := ""
	if manifest.Lead != "" {
		lead, exists := byName[manifest.Lead]
		if !exists {
			log.Errorf("Error: the lead %q is not a session\n", manifest.Lead)
			return 1
		}
		leadID = lead.ID
	}
	group := core.Group{
		Name:       manifest.Name,
		Goal:       manifest.Goal,
		LeadID:     leadID,
		MaxMembers: manifest.MaxMembers,
	}
	if err := gs.CreateGroup(group, ids); err != nil {
		log.Errorf("Error creating group: %v\n", err)
		return 1
	}
	leadNote := "no lead"
	if manifest.Lead != "" {
		leadNote = "lead " + manifest.Lead
	}
	fmt.Fprintf(stdout, "created group %s with %d member(s); %s\n", manifest.Name, len(ids), leadNote)
	return 0
}

func groupAdd(group, name string, stdout io.Writer, log *tui.Logger) int {
	store, gs, ok := openGroupStore(log)
	if !ok {
		return 1
	}
	defer store.Close()
	g, found, ok := groupByName(gs, group)
	if !ok || !found {
		log.Errorf("Error: group %q not found\n", group)
		return 1
	}
	meta, err := store.Get(name)
	if err != nil {
		log.Errorf("Error: %q is not a session\n", name)
		return 1
	}
	if meta.Meta.Mode != session.ModeService {
		log.Errorf("Error: %q is a chat session; a group member must be a service session\n", name)
		return 1
	}
	if err := gs.AddGroupMember(g.ID, meta.Meta.ID); err != nil {
		log.Errorf("Error adding %s to %s: %v\n", name, group, err)
		return 1
	}
	fmt.Fprintf(stdout, "added %s to group %s\n", name, group)
	return 0
}

func groupRemoveMember(group, name string, stdout io.Writer, log *tui.Logger) int {
	store, gs, ok := openGroupStore(log)
	if !ok {
		return 1
	}
	defer store.Close()
	g, found, ok := groupByName(gs, group)
	if !ok || !found {
		log.Errorf("Error: group %q not found\n", group)
		return 1
	}
	meta, err := store.Get(name)
	if err != nil {
		log.Errorf("Error: %q is not a session\n", name)
		return 1
	}
	if err := gs.RemoveGroupMember(g.ID, meta.Meta.ID); err != nil {
		log.Errorf("Error removing %s from %s: %v\n", name, group, err)
		return 1
	}
	fmt.Fprintf(stdout, "removed %s from group %s; the session itself is unchanged\n", name, group)
	return 0
}

func groupDelete(name string, stdout io.Writer, log *tui.Logger) int {
	store, gs, ok := openGroupStore(log)
	if !ok {
		return 1
	}
	defer store.Close()
	if err := gs.DeleteGroup(name); err != nil {
		log.Errorf("Error deleting group %s: %v\n", name, err)
		return 1
	}
	fmt.Fprintf(stdout, "deleted group %s; its member sessions are untouched\n", name)
	return 0
}

// groupSetState applies the same durable transition as `pai service start|stop`
// to every member. It requires the store lock, so it runs while `pai serve` is
// stopped; a running server picks the new state up for groups it registers at
// startup.
func groupSetState(name, state string, stdout io.Writer, log *tui.Logger) int {
	store, gs, ok := openGroupStore(log)
	if !ok {
		return 1
	}
	defer store.Close()
	ss, ok := store.(session.ServiceStore)
	if !ok {
		log.Errorf("Error: this session store does not expose service state\n")
		return 1
	}
	g, found, _ := groupByName(gs, name)
	if !found {
		log.Errorf("Error: group %q not found\n", name)
		return 1
	}
	members, err := gs.GroupMembers(g.ID)
	if err != nil {
		log.Errorf("Error reading members: %v\n", err)
		return 1
	}
	for _, m := range members {
		switch state {
		case runner.StateStopped:
			if err := ss.SetServiceState(m.Name, runner.StateStopped, "stopped with group "+g.Name); err != nil {
				log.Errorf("Error stopping %s: %v\n", m.Name, err)
				return 1
			}
			if err := ss.SetNextWake(m.Name, nil); err != nil {
				log.Errorf("Error clearing the next wake of %s: %v\n", m.Name, err)
				return 1
			}
		case runner.StateSuspended:
			now := time.Now()
			if err := ss.SetServiceState(m.Name, runner.StateSuspended, ""); err != nil {
				log.Errorf("Error starting %s: %v\n", m.Name, err)
				return 1
			}
			if err := ss.SetNextWake(m.Name, &now); err != nil {
				log.Errorf("Error scheduling %s: %v\n", m.Name, err)
				return 1
			}
		}
	}
	verb := "started"
	if state == runner.StateStopped {
		verb = "stopped"
	}
	fmt.Fprintf(stdout, "%s %d member(s) of group %s\n", verb, len(members), g.Name)
	return 0
}
