package memory

import (
	"context"
	"encoding/json"
	"path/filepath"

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
