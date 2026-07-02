package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flakerimi/harness/tool"
)

// NewEnqueueTool exposes the queue to the agent itself: background_task lets it
// take on long work without blocking the conversation — "I'll work on this and
// get back to you". The queued job runs as this same identity (its memory,
// skills, and workspace), and the result is delivered to the surface's deliver
// target (e.g. the Telegram chat that asked) or kept for `harness task show`.
func NewEnqueueTool(store *Store, profileName, providerSlug, deliver string) tool.Tool {
	return enqueueTool{store: store, profile: profileName, provider: providerSlug, deliver: deliver}
}

type enqueueTool struct {
	store    *Store
	profile  string
	provider string
	deliver  string
}

func (e enqueueTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "background_task",
		Description: "Queue a task to work on in the background as a separate run of yourself (same identity, memory, and workspace). Use for long or multi-step work the user shouldn't wait on; the result is delivered to them when done. The prompt must be fully self-contained — the background run has no other context from this conversation.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "The complete task, self-contained: goal, inputs, and what the final output should look like.",
				},
			},
			"required": []string{"prompt"},
		},
	}
}

func (e enqueueTool) Run(ctx context.Context, input json.RawMessage, env *tool.Env) (tool.Result, error) {
	var args struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	t, err := e.store.Enqueue(Task{
		Profile:  e.profile,
		Provider: e.provider,
		Prompt:   args.Prompt,
		Deliver:  e.deliver,
	})
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	where := "the result will be kept for `harness task show`"
	if e.deliver != "" {
		where = "the result will be delivered when done"
	}
	return tool.Result{Content: fmt.Sprintf("queued background task %s — %s", t.ID, where)}, nil
}

// NewStatusTool lets the identity see its own background work — task_status
// lists its recent jobs with status and outcome, so it can report on what it
// finished, notice failures, and follow up. Read-only.
func NewStatusTool(store *Store, profileName string) tool.Tool {
	return statusTool{store: store, profile: profileName}
}

type statusTool struct {
	store   *Store
	profile string
}

func (s statusTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "task_status",
		Description: "Check on your background tasks: lists your recent queued/running/done/failed jobs with their outcomes. Use to report progress, notice failed work worth retrying, or fetch a finished result.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{
					"type":        "integer",
					"description": "How many recent tasks to show (default 10).",
				},
			},
		},
	}
}

// statusResultCap bounds how much of a finished result is inlined per task.
const statusResultCap = 700

func (s statusTool) Run(ctx context.Context, input json.RawMessage, env *tool.Env) (tool.Result, error) {
	var args struct {
		Limit int `json:"limit"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return tool.Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
		}
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 10
	}
	all, err := s.store.List()
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	var mine []Task
	for _, t := range all {
		if t.Profile == s.profile {
			mine = append(mine, t)
		}
	}
	if len(mine) == 0 {
		return tool.Result{Content: "no background tasks for this identity"}, nil
	}
	if len(mine) > limit {
		mine = mine[len(mine)-limit:] // most recent
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d task(s), oldest first:\n", len(mine))
	for _, t := range mine {
		fmt.Fprintf(&b, "\n[%s] %s — %s\n", t.Status, t.ID, clip(t.Prompt, 100))
		switch {
		case t.Error != "":
			fmt.Fprintf(&b, "  error: %s\n", clip(t.Error, 300))
		case t.Result != "":
			fmt.Fprintf(&b, "  result: %s\n", clip(t.Result, statusResultCap))
		}
	}
	return tool.Result{Content: strings.TrimSpace(b.String())}, nil
}

// clip shortens s to max runes with an ellipsis.
func clip(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
