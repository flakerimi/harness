package session

import (
	"testing"

	"github.com/flakerimi/harness/provider"
)

func userMsg(text string) provider.Message {
	return provider.Message{Role: "user", Content: []provider.Block{{Type: provider.BlockText, Text: text}}}
}

func asstMsg(text string) provider.Message {
	return provider.Message{Role: "assistant", Content: []provider.Block{{Type: provider.BlockText, Text: text}}}
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	st := NewStore(t.TempDir())
	sess, err := st.Load("default")
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != "default" || len(sess.History) != 0 {
		t.Errorf("missing session should be empty: %+v", sess)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	st := NewStore(t.TempDir())
	sess := &Session{ID: "trip", History: []provider.Message{
		userMsg("hi"), asstMsg("hello"),
		userMsg("book a flight"),
		{Role: "assistant", Content: []provider.Block{{
			Type:    provider.BlockToolUse,
			ToolUse: &provider.ToolUseBlock{ID: "t1", Name: "search", Input: map[string]any{"q": "flights"}},
		}}},
		{Role: "tool", Content: []provider.Block{{
			Type:       provider.BlockToolResult,
			ToolResult: &provider.ToolResultBlock{ToolUseID: "t1", Content: "found 3"},
		}}},
	}}
	if err := st.Save(sess); err != nil {
		t.Fatal(err)
	}

	got, err := st.Load("trip")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.History) != len(sess.History) {
		t.Fatalf("history length = %d, want %d", len(got.History), len(sess.History))
	}
	if got.Turns() != 2 {
		t.Errorf("Turns() = %d, want 2", got.Turns())
	}
	tu := got.History[3].Content[0].ToolUse
	if tu == nil || tu.Name != "search" || tu.Input["q"] != "flights" {
		t.Errorf("tool_use block not preserved: %+v", got.History[3])
	}
	tr := got.History[4].Content[0].ToolResult
	if tr == nil || tr.Content != "found 3" {
		t.Errorf("tool_result block not preserved: %+v", got.History[4])
	}
}

func TestResetAndList(t *testing.T) {
	st := NewStore(t.TempDir())
	_ = st.Save(&Session{ID: "a", History: []provider.Message{userMsg("x")}})
	_ = st.Save(&Session{ID: "b", History: []provider.Message{userMsg("y"), asstMsg("z"), userMsg("w")}})

	metas, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 || metas[0].ID != "a" || metas[1].ID != "b" {
		t.Fatalf("unexpected list: %+v", metas)
	}
	if metas[1].Turns != 2 {
		t.Errorf("session b turns = %d, want 2", metas[1].Turns)
	}

	if err := st.Reset("a"); err != nil {
		t.Fatal(err)
	}
	sess, _ := st.Load("a")
	if len(sess.History) != 0 {
		t.Errorf("reset session should be empty, got %d msgs", len(sess.History))
	}
}

func TestSanitizeID(t *testing.T) {
	cases := map[string]string{
		"":            "default",
		"  ":          "default",
		"work trip":   "work-trip",
		"../../etc":   "etc",
		"a/b":         "a-b",
		"Keep_Me-123": "Keep_Me-123",
	}
	for in, want := range cases {
		if got := sanitizeID(in); got != want {
			t.Errorf("sanitizeID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListPrefersStoredTitle(t *testing.T) {
	st := NewStore(t.TempDir())
	sess, _ := st.Load("titled")
	sess.Title = "Fix the boiler"
	sess.History = []provider.Message{{Role: "user", Content: []provider.Block{{Type: provider.BlockText, Text: "my boiler is broken and leaks"}}}}
	if err := st.Save(sess); err != nil {
		t.Fatal(err)
	}
	metas, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Title != "Fix the boiler" {
		t.Fatalf("metas = %+v, want stored title preferred", metas)
	}
}
