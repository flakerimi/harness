package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/router"
	"github.com/flakerimi/harness/tool"
)

// WorkerConfig configures one dispatchable specialist sub-agent.
type WorkerConfig struct {
	Description string
	Tier        router.Tier
	System      string
	Tools       *tool.Registry // the subset this specialist may use
}

// Dispatch is a tool that routes a focused task to a named specialist sub-agent
// from a roster. The roster is advertised in the orchestrator's system prompt
// (see subagent.DiscoveryText); the model calls dispatch(agent, task) and gets
// the specialist's result back. This is the pluggable generalization of the
// single-worker Delegate tool.
type Dispatch struct {
	prov      provider.Provider
	router    router.Router
	maxTokens int
	caps      []string
	workers   map[string]WorkerConfig
	names     []string
}

// NewDispatch builds the dispatch tool over a roster of specialists.
func NewDispatch(prov provider.Provider, rt router.Router, maxTokens int, caps []string, workers map[string]WorkerConfig) *Dispatch {
	names := make([]string, 0, len(workers))
	for n := range workers {
		names = append(names, n)
	}
	sort.Strings(names)
	return &Dispatch{prov: prov, router: rt, maxTokens: maxTokens, caps: caps, workers: workers, names: names}
}

func (d *Dispatch) Spec() tool.Spec {
	return tool.Spec{
		Name:        "dispatch",
		Description: "Hand one focused, self-contained task to a specialist sub-agent (see the Specialists list). It runs separately and returns its result. Pick the specialist whose description best fits.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{
					"type":        "string",
					"description": "Which specialist to use.",
					"enum":        d.names,
				},
				"task": map[string]any{
					"type":        "string",
					"description": "The task, fully self-contained (the specialist has no other context).",
				},
			},
			"required": []string{"agent", "task"},
		},
	}
}

func (d *Dispatch) Run(ctx context.Context, input json.RawMessage, env *tool.Env) (tool.Result, error) {
	var args struct {
		Agent string `json:"agent"`
		Task  string `json:"task"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(args.Task) == "" {
		return tool.Result{Content: "task is required", IsError: true}, nil
	}
	w, ok := d.workers[args.Agent]
	if !ok {
		return tool.Result{Content: fmt.Sprintf("unknown specialist %q (available: %s)", args.Agent, strings.Join(d.names, ", ")), IsError: true}, nil
	}

	worker := New(d.prov, w.Tools, Options{
		System:    w.System,
		Router:    d.router,
		BaseTier:  w.Tier,
		Escalate:  true,
		MaxTokens: d.maxTokens,
		Caps:      d.caps,
		Env:       env,
	})

	var out strings.Builder
	if err := worker.Run(ctx, args.Task, &collectHandler{out: &out}); err != nil {
		return tool.Result{Content: "specialist error: " + err.Error(), IsError: true}, nil
	}
	return tool.Result{Content: strings.TrimSpace(out.String())}, nil
}
