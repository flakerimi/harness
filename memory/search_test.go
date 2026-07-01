package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func seed(t *testing.T) *Store {
	t.Helper()
	store := NewStore(t.TempDir())
	save := func(name, content string) {
		if _, err := store.Save(name, content); err != nil {
			t.Fatal(err)
		}
	}
	save("harness-project", "Building an agent harness in Go with a memory system.")
	save("terse", "The user prefers terse, direct answers.")
	save("client-acme", "Acme Corp is a client; their main contact is Dardan.")
	save("lives", "Lives in Prishtina, Kosovo.")
	return store
}

func TestSearchRanksAndLimits(t *testing.T) {
	store := seed(t)

	hits, err := store.Search("acme client contact", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Name != "client-acme" {
		t.Fatalf("expected client-acme first, got %+v", hits)
	}

	// A name-token match outranks a body-only match.
	hits, _ = store.Search("harness", 5)
	if len(hits) == 0 || hits[0].Name != "harness-project" {
		t.Fatalf("name match should rank first, got %+v", hits)
	}

	// limit is respected.
	if got, _ := store.Search("the user in go", 2); len(got) > 2 {
		t.Errorf("limit not applied: got %d", len(got))
	}
}

func TestSearchNoMatch(t *testing.T) {
	store := seed(t)
	if hits, _ := store.Search("bicycle repair", 5); len(hits) != 0 {
		t.Errorf("unrelated query should match nothing, got %+v", hits)
	}
	// A query of only stop words / short tokens yields nothing rather than all.
	if hits, _ := store.Search("the a of", 5); len(hits) != 0 {
		t.Errorf("stop-word-only query should match nothing, got %+v", hits)
	}
}

func TestSearchPrefixStemming(t *testing.T) {
	store := seed(t)
	// "projects" (plural) should still find the "harness-project" note.
	if hits, _ := store.Search("projects", 5); len(hits) == 0 || hits[0].Name != "harness-project" {
		t.Errorf("prefix/plural match failed: %+v", hits)
	}
}

func TestRecallTool(t *testing.T) {
	store := seed(t)
	tl := NewRecallTool(store)

	in, _ := json.Marshal(map[string]any{"query": "acme"})
	res, err := tl.Run(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "Acme Corp") {
		t.Fatalf("recall returned %q err=%v", res.Content, res.IsError)
	}

	miss, _ := json.Marshal(map[string]any{"query": "bicycle"})
	res2, _ := tl.Run(context.Background(), miss, nil)
	if res2.IsError || !strings.Contains(res2.Content, "no matching") {
		t.Errorf("empty recall should say so, got %q", res2.Content)
	}
}

func TestDigestBounds(t *testing.T) {
	mems := []Memory{
		{Name: "a", Content: "one"},
		{Name: "b", Content: "two"},
		{Name: "c", Content: "three"},
	}
	// Under the cap: everything shown, no recall note.
	full := Digest(mems, 0)
	if !strings.Contains(full, "one") || !strings.Contains(full, "three") || strings.Contains(full, "recall tool") {
		t.Errorf("unbounded digest wrong: %q", full)
	}
	// Over the cap: truncated with a recall pointer.
	capped := Digest(mems, 2)
	if !strings.Contains(capped, "one") || strings.Contains(capped, "three") {
		t.Errorf("capped digest should show first 2 only: %q", capped)
	}
	if !strings.Contains(capped, "and 1 more") || !strings.Contains(capped, "recall tool") {
		t.Errorf("capped digest should point to recall: %q", capped)
	}
}
