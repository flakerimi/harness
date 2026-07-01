package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flakerimi/harness/tool"
)

// RememberTool persists a durable fact about the user to the profile's memory.
type RememberTool struct {
	store *Store
}

// NewRememberTool builds the remember tool over a memory store.
func NewRememberTool(store *Store) *RememberTool { return &RememberTool{store: store} }

func (RememberTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "remember",
		Description: "Save a durable fact or preference about the user for future conversations.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{"type": "string", "description": "The fact to remember, as a self-contained sentence."},
				"name":    map[string]any{"type": "string", "description": "Optional short name/slug for this memory."},
			},
			"required": []string{"content"},
		},
	}
}

func (t *RememberTool) Run(_ context.Context, input json.RawMessage, _ *tool.Env) (tool.Result, error) {
	var args struct {
		Content string `json:"content"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	path, err := t.store.Save(args.Name, args.Content)
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	return tool.Result{Content: "remembered (" + filepath.Base(path) + ")"}, nil
}

// RecallTool searches the profile's stored memories for ones relevant to a
// query — the retrieval half of the memory loop, so the agent can pull up a
// past note on demand instead of relying on everything sitting in-context.
type RecallTool struct {
	store *Store
}

// NewRecallTool builds the recall tool over a memory store.
func NewRecallTool(store *Store) *RecallTool { return &RecallTool{store: store} }

func (RecallTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "recall",
		Description: "Search your saved memories for ones relevant to a query. Use this when the user refers to something they told you before — a past idea, a person, an ongoing project, a preference — that isn't already in front of you.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "What to look for — a topic, name, or keywords."},
				"limit": map[string]any{"type": "integer", "description": "Max results to return (default 5)."},
			},
			"required": []string{"query"},
		},
	}
}

func (t *RecallTool) Run(_ context.Context, input json.RawMessage, _ *tool.Env) (tool.Result, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	mems, err := t.store.Search(args.Query, args.Limit)
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	if len(mems) == 0 {
		return tool.Result{Content: "no matching memories."}, nil
	}
	var b strings.Builder
	for _, m := range mems {
		fmt.Fprintf(&b, "- %s: %s\n", m.Name, oneLine(m.Content))
	}
	return tool.Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}
