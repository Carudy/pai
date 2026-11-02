package role

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
)

// The board tools are ordinary built-ins; the host binds the caller, so every
// payload names a task and never a session or a group.

type taskAddPayload struct {
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Priority int    `json:"priority"`
}

func runTaskAdd(_ context.Context, _ *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error) {
	var p taskAddPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", fmt.Errorf("task_add payload: %w", err)
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		return "", errors.New("task_add needs a title")
	}
	rt.Observer.ToolCall(core.ToolCall{Name: "task_add", Target: title, Detail: p.Detail, Reason: reason})
	if rt.Tasks == nil {
		return "[tool error]\nTOOL: task_add\nERROR: this run has no group board", nil
	}
	t, err := rt.Tasks.Add(title, strings.TrimSpace(p.Detail), p.Priority)
	if err != nil {
		rt.Observer.ToolResult(core.ToolResult{Message: "Task add failed: " + err.Error()})
		return fmt.Sprintf("[tool error]\nTOOL: task_add\nERROR: %v", err), nil
	}
	rt.Observer.ToolResult(core.ToolResult{OK: true, Message: "task " + t.ID + " added"})
	return "[task added]\n" + renderTask(t), nil
}

type taskClaimPayload struct {
	ID string `json:"id"`
}

func runTaskClaim(_ context.Context, _ *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error) {
	var p taskClaimPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", fmt.Errorf("task_claim payload: %w", err)
	}
	id := strings.TrimSpace(p.ID)
	if id == "" {
		return "", errors.New("task_claim needs an id")
	}
	rt.Observer.ToolCall(core.ToolCall{Name: "task_claim", Target: id, Reason: reason})
	if rt.Tasks == nil {
		return "[tool error]\nTOOL: task_claim\nERROR: this run has no group board", nil
	}
	t, err := rt.Tasks.Claim(id)
	if err != nil {
		rt.Observer.ToolResult(core.ToolResult{Message: "Task claim failed: " + err.Error()})
		return fmt.Sprintf("[tool error]\nTOOL: task_claim\nID: %s\nERROR: %v", id, err), nil
	}
	rt.Observer.ToolResult(core.ToolResult{OK: true, Message: "task " + t.ID + " claimed"})
	return "[task claimed]\n" + renderTask(t), nil
}

type taskUpdatePayload struct {
	ID            string `json:"id"`
	ExpectVersion int    `json:"expect_version"`
	Status        string `json:"status"`
	Note          string `json:"note"`
}

func runTaskUpdate(_ context.Context, _ *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error) {
	var p taskUpdatePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", fmt.Errorf("task_update payload: %w", err)
	}
	id, status := strings.TrimSpace(p.ID), strings.TrimSpace(p.Status)
	if id == "" {
		return "", errors.New("task_update needs an id")
	}
	if status == "" {
		return "", errors.New("task_update needs a status")
	}
	if p.ExpectVersion <= 0 {
		return "", errors.New("task_update needs expect_version; read the task first")
	}
	rt.Observer.ToolCall(core.ToolCall{Name: "task_update", Target: id, Detail: status, Reason: reason})
	if rt.Tasks == nil {
		return "[tool error]\nTOOL: task_update\nERROR: this run has no group board", nil
	}
	t, err := rt.Tasks.Update(id, p.ExpectVersion, core.TaskStatus(status), strings.TrimSpace(p.Note))
	if err != nil {
		rt.Observer.ToolResult(core.ToolResult{Message: "Task update failed: " + err.Error()})
		return fmt.Sprintf("[tool error]\nTOOL: task_update\nID: %s\nERROR: %v", id, err), nil
	}
	rt.Observer.ToolResult(core.ToolResult{OK: true, Message: "task " + t.ID + " is " + string(t.Status)})
	return "[task updated]\n" + renderTask(t), nil
}

type taskListPayload struct {
	Status string `json:"status"`
	Limit  int    `json:"limit"`
}

func runTaskList(_ context.Context, _ *config.UserConfig, rt *Runtime, reason string, payload json.RawMessage) (string, error) {
	var p taskListPayload
	if len(strings.TrimSpace(string(payload))) > 0 {
		if err := json.Unmarshal(payload, &p); err != nil {
			return "", fmt.Errorf("task_list payload: %w", err)
		}
	}
	rt.Observer.ToolCall(core.ToolCall{Name: "task_list", Target: strings.TrimSpace(p.Status), Reason: reason})
	if rt.Tasks == nil {
		return "[tool error]\nTOOL: task_list\nERROR: this run has no group board", nil
	}
	tasks, err := rt.Tasks.List(core.TaskStatus(strings.TrimSpace(p.Status)), p.Limit)
	if err != nil {
		rt.Observer.ToolResult(core.ToolResult{Message: "Task list failed: " + err.Error()})
		return fmt.Sprintf("[tool error]\nTOOL: task_list\nERROR: %v", err), nil
	}
	rt.Observer.ToolResult(core.ToolResult{OK: true, Message: fmt.Sprintf("%d task(s)", len(tasks))})
	if len(tasks) == 0 {
		return "[tasks]\nnone", nil
	}
	var b strings.Builder
	b.WriteString("[tasks]\n")
	for _, t := range tasks {
		b.WriteString(renderTask(t))
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// renderTask is one board row: id, status, owner, version, title. The version is
// part of the line because the next write must carry it.
func renderTask(t core.Task) string {
	owner := "-"
	if t.OwnerName != "" {
		owner = t.OwnerName
	}
	line := fmt.Sprintf("%s  %-9s owner=%s v%d  %s", t.ID, t.Status, owner, t.Version, t.Title)
	if t.Status == core.TaskClaimed && !t.LeaseUntil.IsZero() {
		line += "  lease_until=" + t.LeaseUntil.Local().Format(time.RFC3339)
	}
	return line
}
