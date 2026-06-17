package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Save("", "The user prefers terse, direct answers."); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("project", "Building an agent harness in Go."); err != nil {
		t.Fatal(err)
	}

	mems, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 2 {
		t.Fatalf("want 2 memories, got %d", len(mems))
	}
	joined := mems[0].Content + " " + mems[1].Content
	if !strings.Contains(joined, "terse") || !strings.Contains(joined, "agent harness") {
		t.Errorf("memories not stored: %+v", mems)
	}
}

func TestRememberTool(t *testing.T) {
	store := NewStore(t.TempDir())
	tl := NewRememberTool(store)

	in, _ := json.Marshal(map[string]string{"content": "Lives in Albania."})
	res, err := tl.Run(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "remembered") {
		t.Fatalf("remember result = %q err=%v", res.Content, res.IsError)
	}

	mems, _ := store.Load()
	if len(mems) != 1 || !strings.Contains(mems[0].Content, "Albania") {
		t.Errorf("memory not persisted: %+v", mems)
	}
}

func TestContext(t *testing.T) {
	if !strings.Contains(Context(nil), "remember tool") {
		t.Error("empty context should still include the remember instruction")
	}
	c := Context([]Memory{{Name: "pref", Content: "Prefers terse answers."}})
	if !strings.Contains(c, "Prefers terse answers.") || !strings.Contains(c, "remember about the user") {
		t.Errorf("context missing pieces: %q", c)
	}
}
