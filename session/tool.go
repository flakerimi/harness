package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flakerimi/harness/tool"
)

// ReviewTool lets the agent read its own recent conversation transcripts — the
// bridge that makes reflection possible from any run (interactive, a scheduled
// nightly task, or `harness reflect`). Without it, sessions are saved but the
// agent can't look back at them.
type ReviewTool struct {
	store *Store
}

// NewReviewTool builds the review_sessions tool over a session store.
func NewReviewTool(store *Store) *ReviewTool { return &ReviewTool{store: store} }

func (ReviewTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "review_sessions",
		Description: "Read your recent conversation transcripts so you can reflect on and learn from them. Returns the most recent sessions as plain dialogue.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{"type": "integer", "description": "How many recent sessions to return (default 3)."},
			},
		},
	}
}

func (t *ReviewTool) Run(_ context.Context, input json.RawMessage, _ *tool.Env) (tool.Result, error) {
	var args struct {
		Limit int `json:"limit"`
	}
	_ = json.Unmarshal(input, &args)
	limit := args.Limit
	if limit <= 0 {
		limit = 3
	}
	sessions, err := t.store.Recent(limit)
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	if len(sessions) == 0 {
		return tool.Result{Content: "no past sessions to review"}, nil
	}
	var b strings.Builder
	for _, s := range sessions {
		fmt.Fprintf(&b, "### session %q (%d turns)\n%s\n\n", s.ID, s.Turns(), Transcript(s.History))
	}
	return tool.Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}
