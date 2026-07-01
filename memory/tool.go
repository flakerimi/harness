package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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
				"tags":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional labels to categorize this memory (e.g. idea, link, person, project)."},
			},
			"required": []string{"content"},
		},
	}
}

func (t *RememberTool) Run(_ context.Context, input json.RawMessage, _ *tool.Env) (tool.Result, error) {
	var args struct {
		Content string   `json:"content"`
		Name    string   `json:"name"`
		Tags    []string `json:"tags"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	// Tags ride along in the body so they're searchable by recall with no
	// separate index or file-format change.
	content := args.Content
	if len(args.Tags) > 0 {
		content = strings.TrimRight(content, "\n") + "\n\nTags: " + strings.Join(args.Tags, ", ")
	}
	path, err := t.store.Save(args.Name, content)
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	return tool.Result{Content: "remembered (" + filepath.Base(path) + ")"}, nil
}

// FeedbackTool records an in-the-moment correction as a durable lesson — so
// when the user pushes back ("no, do it this way") it shapes future behavior
// instead of evaporating at the end of the session. Lessons are memories tagged
// "lesson", which Digest surfaces in a dedicated "apply these" section.
type FeedbackTool struct {
	store *Store
}

// NewFeedbackTool builds the record_feedback tool over a memory store.
func NewFeedbackTool(store *Store) *FeedbackTool { return &FeedbackTool{store: store} }

func (FeedbackTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "record_feedback",
		Description: "Record a correction or standing preference the user just gave, as a durable lesson to apply next time. Call this whenever they push back, tell you to do something differently, or state how they want things done — so the lesson survives beyond this conversation.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"situation": map[string]any{"type": "string", "description": "When this applies — the context or trigger (e.g. 'drafting emails')."},
				"lesson":    map[string]any{"type": "string", "description": "What to do differently or the preference to follow, as a directive."},
			},
			"required": []string{"lesson"},
		},
	}
}

func (t *FeedbackTool) Run(_ context.Context, input json.RawMessage, _ *tool.Env) (tool.Result, error) {
	var args struct {
		Situation string `json:"situation"`
		Lesson    string `json:"lesson"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	lesson := strings.TrimSpace(args.Lesson)
	if lesson == "" {
		return tool.Result{Content: "lesson is required", IsError: true}, nil
	}
	content := lesson
	if s := strings.TrimSpace(args.Situation); s != "" {
		content = "When " + s + ": " + lesson
	}
	content += "\n\nTags: lesson, feedback"
	path, err := t.store.Save("", content)
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	return tool.Result{Content: "recorded lesson (" + filepath.Base(path) + ")"}, nil
}

// ResurfaceTool surfaces a memory worth proactively revisiting — the engine of
// the "resurface when relevant" loop for scheduled, unprompted check-ins. It
// returns an aging note and rotates it to the back of the queue, so a run works
// through the whole store over time rather than repeating one item.
type ResurfaceTool struct {
	store  *Store
	minAge time.Duration
}

// NewResurfaceTool builds the resurface tool. minAge is how old a memory must be
// before it's eligible (so fresh captures aren't parroted straight back); a
// value <= 0 uses 24h.
func NewResurfaceTool(store *Store, minAge time.Duration) *ResurfaceTool {
	if minAge <= 0 {
		minAge = 24 * time.Hour
	}
	return &ResurfaceTool{store: store, minAge: minAge}
}

func (ResurfaceTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "resurface",
		Description: "Pick one saved memory worth proactively revisiting right now — for a check-in, reflection, or gentle nudge. Returns an aging note (and rotates it so you don't repeat it), or 'nothing to resurface' when there's nothing suitable.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func (t *ResurfaceTool) Run(_ context.Context, _ json.RawMessage, _ *tool.Env) (tool.Result, error) {
	now := time.Now()
	m, ok := t.store.Resurface(t.minAge, now)
	if !ok {
		return tool.Result{Content: "nothing to resurface"}, nil
	}
	_ = t.store.Touch(m.Name, now) // rotate to the back; best-effort
	return tool.Result{Content: fmt.Sprintf("%s: %s", m.Name, oneLine(m.Content))}, nil
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
