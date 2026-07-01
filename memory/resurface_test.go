package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// age sets a memory file's mtime to d ago, to control resurfacing order.
func age(t *testing.T, store *Store, name string, d time.Duration) {
	t.Helper()
	path := filepath.Join(store.Dir(), name+".md")
	when := time.Now().Add(-d)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestResurfacePicksOldestEligible(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, n := range []string{"fresh", "old", "oldest"} {
		if _, err := store.Save(n, "note "+n); err != nil {
			t.Fatal(err)
		}
	}
	age(t, store, "fresh", 1*time.Hour)      // too new
	age(t, store, "old", 3*24*time.Hour)     // eligible
	age(t, store, "oldest", 10*24*time.Hour) // eligible, least recent

	m, ok := store.Resurface(24*time.Hour, time.Now())
	if !ok || m.Name != "oldest" {
		t.Fatalf("expected 'oldest', got %q ok=%v", m.Name, ok)
	}
}

func TestResurfaceNothingEligible(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Save("recent", "just saved"); err != nil {
		t.Fatal(err)
	}
	// Everything is newer than minAge → nothing to resurface.
	if _, ok := store.Resurface(24*time.Hour, time.Now()); ok {
		t.Error("no memory should be eligible")
	}
	// Empty store → not ok.
	empty := NewStore(t.TempDir())
	if _, ok := empty.Resurface(0, time.Now()); ok {
		t.Error("empty store should resurface nothing")
	}
}

func TestTouchRotates(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, n := range []string{"a", "b"} {
		if _, err := store.Save(n, "note "+n); err != nil {
			t.Fatal(err)
		}
	}
	age(t, store, "a", 10*24*time.Hour)
	age(t, store, "b", 5*24*time.Hour)

	// "a" is oldest, so it surfaces first.
	m, _ := store.Resurface(24*time.Hour, time.Now())
	if m.Name != "a" {
		t.Fatalf("first pick = %q, want a", m.Name)
	}
	// Touching it rotates it to the back; now "b" is the oldest eligible.
	if err := store.Touch("a", time.Now()); err != nil {
		t.Fatal(err)
	}
	m2, _ := store.Resurface(24*time.Hour, time.Now())
	if m2.Name != "b" {
		t.Fatalf("after touch, pick = %q, want b", m2.Name)
	}
}

func TestResurfaceTool(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Save("kosovo-trip", "Wanted to plan a trip to the Rugova valley."); err != nil {
		t.Fatal(err)
	}
	age(t, store, "kosovo-trip", 5*24*time.Hour)

	tl := NewResurfaceTool(store, 24*time.Hour)
	res, err := tl.Run(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "Rugova") {
		t.Fatalf("resurface returned %q err=%v", res.Content, res.IsError)
	}

	// Nothing eligible → a clear, non-error signal.
	empty := NewResurfaceTool(NewStore(t.TempDir()), 24*time.Hour)
	res2, _ := empty.Run(context.Background(), nil, nil)
	if res2.IsError || !strings.Contains(res2.Content, "nothing to resurface") {
		t.Errorf("empty resurface = %q", res2.Content)
	}
}

func TestRememberWithTags(t *testing.T) {
	store := NewStore(t.TempDir())
	tl := NewRememberTool(store)

	in, _ := json.Marshal(map[string]any{
		"content": "Duxt is a Dart web framework worth trying.",
		"name":    "duxt",
		"tags":    []string{"link", "idea"},
	})
	if res, _ := tl.Run(context.Background(), in, nil); res.IsError {
		t.Fatalf("remember failed: %q", res.Content)
	}

	// Tags land in the body, so recall finds the memory by tag.
	hits, _ := store.Search("idea", 5)
	if len(hits) == 0 || hits[0].Name != "duxt" {
		t.Fatalf("tag not searchable: %+v", hits)
	}
	if !strings.Contains(hits[0].Content, "Tags: link, idea") {
		t.Errorf("tags not stored in body: %q", hits[0].Content)
	}
}
