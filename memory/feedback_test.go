package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestFeedbackTool(t *testing.T) {
	store := NewStore(t.TempDir())
	tl := NewFeedbackTool(store)

	in, _ := json.Marshal(map[string]any{
		"situation": "drafting emails",
		"lesson":    "keep them to two sentences",
	})
	res, err := tl.Run(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "recorded lesson") {
		t.Fatalf("feedback result = %q err=%v", res.Content, res.IsError)
	}

	mems, _ := store.Load()
	if len(mems) != 1 {
		t.Fatalf("want 1 lesson, got %d", len(mems))
	}
	m := mems[0]
	if !strings.Contains(m.Content, "When drafting emails: keep them to two sentences") {
		t.Errorf("lesson body wrong: %q", m.Content)
	}
	if !isLesson(m) {
		t.Error("recorded feedback should be tagged as a lesson")
	}

	// Missing lesson is a validation error.
	bad, _ := json.Marshal(map[string]any{"situation": "x"})
	if r, _ := tl.Run(context.Background(), bad, nil); !r.IsError {
		t.Error("missing lesson should error")
	}
}

func TestDigestSeparatesLessonsFromFacts(t *testing.T) {
	mems := []Memory{
		{Name: "lives", Content: "Lives in Prishtina."},
		{Name: "terse", Content: "When drafting emails: keep them to two sentences\n\nTags: lesson, feedback"},
	}
	d := Digest(mems, 0)

	if !strings.Contains(d, "## Lessons you've learned (apply these)") {
		t.Errorf("digest missing lessons section:\n%s", d)
	}
	if !strings.Contains(d, "## What you remember about the user") {
		t.Errorf("digest missing facts section:\n%s", d)
	}
	// The lesson appears under Lessons, above the facts header.
	li := strings.Index(d, "keep them to two sentences")
	fi := strings.Index(d, "What you remember about the user")
	if li < 0 || fi < 0 || li > fi {
		t.Errorf("lesson should appear before the facts section:\n%s", d)
	}
	// The Tags line is stripped from the displayed lesson.
	if strings.Contains(d, "Tags: lesson") {
		t.Errorf("digest should not show the raw Tags line:\n%s", d)
	}
}
