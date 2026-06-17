package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/router"
	"github.com/flakerimi/harness/tool"
)

// Delegate is a tool that runs a focused subtask on a fresh sub-agent at a
// chosen tier, over a given toolset. It lets a reasoning-tier orchestrator hand
// grunt work — researching one person, extracting fields — to a fast-tier
// worker, so the expensive model only does the synthesis.
type Delegate struct {
	ToolName    string            // tool name; default "delegate"
	Description string            // tool description (optional)
	Provider    provider.Provider // worker provider (usually the same as the orchestrator)
	Tools       *tool.Registry    // tools the worker may use
	Router      router.Router     // resolves the worker's tier → model
	Tier        router.Tier       // tier the worker runs at (e.g. fast)
	System      string            // worker persona
	MaxTokens   int
	Caps        []string
}

func (d Delegate) Spec() tool.Spec {
	name := d.ToolName
	if name == "" {
		name = "delegate"
	}
	desc := d.Description
	if desc == "" {
		desc = "Delegate one focused, self-contained subtask (e.g. research a single person or company) to a worker agent. Returns its findings as text. Call once per subtask; you may call it several times."
	}
	return tool.Spec{
		Name:        name,
		Description: desc,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task": map[string]any{
					"type":        "string",
					"description": "The subtask, fully self-contained (the worker has no other context).",
				},
			},
			"required": []string{"task"},
		},
	}
}

func (d Delegate) Run(ctx context.Context, input json.RawMessage, env *tool.Env) (tool.Result, error) {
	var args struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(args.Task) == "" {
		return tool.Result{Content: "task is required", IsError: true}, nil
	}

	worker := New(d.Provider, d.Tools, Options{
		System:    d.System,
		Router:    d.Router,
		BaseTier:  d.Tier,
		Escalate:  true, // a fast worker can still escalate if it stalls
		MaxTokens: d.MaxTokens,
		Caps:      d.Caps,
		Env:       env,
	})

	var out strings.Builder
	if err := worker.Run(ctx, args.Task, &collectHandler{out: &out}); err != nil {
		return tool.Result{Content: "subtask error: " + err.Error(), IsError: true}, nil
	}
	return tool.Result{Content: strings.TrimSpace(out.String())}, nil
}

// collectHandler captures a sub-agent's text output and ignores the rest.
type collectHandler struct{ out *strings.Builder }

func (c *collectHandler) OnText(d string)                      { c.out.WriteString(d) }
func (c *collectHandler) OnToolStart(_, _ string)              {}
func (c *collectHandler) OnToolResult(_ string, _ tool.Result) {}
func (c *collectHandler) OnUsage(_ provider.Usage)             {}
func (c *collectHandler) OnStop(_ string)                      {}
