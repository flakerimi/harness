package task

import (
	"context"
	"encoding/json"
	"fmt"

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
