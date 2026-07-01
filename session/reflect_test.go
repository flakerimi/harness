package session

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flakerimi/harness/provider"
)

// touchNewer sets a session file's mtime into the future so it sorts as the most
// recently modified regardless of save order.
func touchNewer(t *testing.T, st *Store, id string) {
	t.Helper()
	when := time.Now().Add(time.Hour)
	if err := os.Chtimes(st.path(id), when, when); err != nil {
		t.Fatal(err)
	}
}

func TestTranscript(t *testing.T) {
	history := []provider.Message{
		userMsg("book a flight"),
		{Role: "assistant", Content: []provider.Block{{
			Type:    provider.BlockToolUse,
			ToolUse: &provider.ToolUseBlock{ID: "t1", Name: "search"},
		}}},
		{Role: "tool", Content: []provider.Block{{
			Type:       provider.BlockToolResult,
			ToolResult: &provider.ToolResultBlock{ToolUseID: "t1", Content: "found 3"},
		}}},
		asstMsg("Booked seat 4A."),
	}
	got := Transcript(history)
	if !strings.Contains(got, "User: book a flight") || !strings.Contains(got, "Assistant: Booked seat 4A.") {
		t.Errorf("transcript missing dialogue: %q", got)
	}
	// Tool-only turns are omitted.
	if strings.Contains(got, "found 3") || strings.Contains(got, "search") {
		t.Errorf("transcript should omit tool traffic: %q", got)
	}
}

func TestRecentOrdersByRecencyAndSkipsEmpty(t *testing.T) {
	st := NewStore(t.TempDir())
	// Two real conversations and one empty session.
	if err := st.Save(&Session{ID: "old", History: []provider.Message{userMsg("a"), asstMsg("b")}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(&Session{ID: "new", History: []provider.Message{userMsg("c"), asstMsg("d")}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(&Session{ID: "empty"}); err != nil {
		t.Fatal(err)
	}
	// Make "new" the most recently modified.
	touchNewer(t, st, "new")

	got, err := st.Recent(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 non-empty sessions, got %d", len(got))
	}
	if got[0].ID != "new" {
		t.Errorf("most recent should be first, got %q", got[0].ID)
	}
}

func TestReviewTool(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Save(&Session{ID: "work", History: []provider.Message{
		userMsg("keep replies terse"), asstMsg("Got it."),
	}}); err != nil {
		t.Fatal(err)
	}

	tl := NewReviewTool(st)
	in, _ := json.Marshal(map[string]any{"limit": 3})
	res, err := tl.Run(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "keep replies terse") || !strings.Contains(res.Content, "session \"work\"") {
		t.Fatalf("review result = %q err=%v", res.Content, res.IsError)
	}

	// No sessions → a clear, non-error signal.
	empty := NewReviewTool(NewStore(t.TempDir()))
	res2, _ := empty.Run(context.Background(), nil, nil)
	if res2.IsError || !strings.Contains(res2.Content, "no past sessions") {
		t.Errorf("empty review = %q", res2.Content)
	}
}
