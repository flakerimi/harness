package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flakerimi/harness/tool"
)

// NewTools exposes the schedule to the agent itself: schedule_add /
// schedule_list / schedule_remove let an identity manage its own recurring
// duties ("brief me at 7", "check X every hour") instead of asking a human to
// edit config. Tools are scoped to the identity — it sees and removes only its
// own tasks. deliver defaults to the surface's reply target so scheduled
// output reaches the person who asked.
func NewTools(store *Store, profileName, providerSlug, deliver string) []tool.Tool {
	s := scheduleTools{store: store, profile: profileName, provider: providerSlug, deliver: deliver}
	return []tool.Tool{addTool{s}, listTool{s}, removeTool{s}}
}

type scheduleTools struct {
	store    *Store
	profile  string
	provider string
	deliver  string
}

type addTool struct{ scheduleTools }

func (a addTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "schedule_add",
		Description: "Schedule a recurring (or one-shot) run of yourself: a prompt executed on a clock, as this same identity. Specs: 'every 30m' | 'daily 07:00' | 'weekly fri 18:00' | 'once 09:00' | 'in 2h' (one-shots retire after firing). Output is delivered to the user. For watch-style schedules, instruct the run to reply with exactly the single word NOTHING when there's nothing worth saying — that is swallowed and no message is sent.",
		Writes:      true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "Stable kebab-case id (e.g. 'morning-brief'). Adding an existing id fails — remove it first to replace.",
				},
				"spec": map[string]any{
					"type":        "string",
					"description": "When to run: 'every 30m' | 'daily 07:00' | 'weekly fri 18:00' | 'once 09:00' | 'in 2h'.",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "The self-contained instruction the scheduled run executes (it has no other context from this conversation).",
				},
				"deliver": map[string]any{
					"type":        "string",
					"description": "Optional: where output goes, overriding the default reply target. Forms: 'telegram:<chatID>', 'push:<profile>', 'webhook:<url>'; combine with | to reach several ('telegram:123|push:personal').",
				},
			},
			"required": []string{"id", "spec", "prompt"},
		},
	}
}

func (a addTool) Run(ctx context.Context, input json.RawMessage, env *tool.Env) (tool.Result, error) {
	var args struct {
		ID      string `json:"id"`
		Spec    string `json:"spec"`
		Prompt  string `json:"prompt"`
		Deliver string `json:"deliver"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if args.ID == "" || args.Spec == "" || strings.TrimSpace(args.Prompt) == "" {
		return tool.Result{Content: "id, spec, and prompt are required", IsError: true}, nil
	}
	deliver := a.deliver
	if strings.TrimSpace(args.Deliver) != "" {
		deliver = strings.TrimSpace(args.Deliver)
	}
	t, err := a.store.Add(Task{
		ID:       args.ID,
		Profile:  a.profile,
		Provider: a.provider,
		Prompt:   args.Prompt,
		Spec:     args.Spec,
		Deliver:  deliver,
	}, time.Now())
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	return tool.Result{Content: fmt.Sprintf("scheduled %s: %s — next run %s", t.ID, t.Spec, t.NextRun.Format("Mon 2006-01-02 15:04"))}, nil
}

type listTool struct{ scheduleTools }

func (l listTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "schedule_list",
		Description: "List your scheduled runs (this identity's): id, spec, next run, and what each does. Check here before adding — don't duplicate an existing schedule.",
		InputSchema: map[string]any{"type": "object"},
	}
}

func (l listTool) Run(ctx context.Context, input json.RawMessage, env *tool.Env) (tool.Result, error) {
	tasks, err := l.store.Load()
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	var b strings.Builder
	n := 0
	for _, t := range tasks {
		if t.Profile != l.profile {
			continue
		}
		n++
		state := "on"
		if !t.Enabled {
			state = "off (fired)"
		}
		fmt.Fprintf(&b, "- %s [%s] %s · next %s\n  %s\n", t.ID, state, t.Spec, t.NextRun.Format("Mon 15:04"), clipPrompt(t.Prompt, 140))
	}
	if n == 0 {
		return tool.Result{Content: "no scheduled runs for this identity"}, nil
	}
	return tool.Result{Content: strings.TrimSpace(b.String())}, nil
}

type removeTool struct{ scheduleTools }

func (r removeTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "schedule_remove",
		Description: "Remove one of your scheduled runs by id (only this identity's own).",
		Writes:      true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "The schedule id to remove."},
			},
			"required": []string{"id"},
		},
	}
}

func (r removeTool) Run(ctx context.Context, input json.RawMessage, env *tool.Env) (tool.Result, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	tasks, err := r.store.Load()
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	for _, t := range tasks {
		if t.ID != args.ID {
			continue
		}
		if t.Profile != r.profile {
			return tool.Result{Content: fmt.Sprintf("schedule %q belongs to identity %q — not yours to remove", args.ID, t.Profile), IsError: true}, nil
		}
		if _, err := r.store.Remove(args.ID); err != nil {
			return tool.Result{Content: err.Error(), IsError: true}, nil
		}
		return tool.Result{Content: "removed " + args.ID}, nil
	}
	return tool.Result{Content: fmt.Sprintf("no schedule %q", args.ID), IsError: true}, nil
}

// clipPrompt shortens a prompt for the listing.
func clipPrompt(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
