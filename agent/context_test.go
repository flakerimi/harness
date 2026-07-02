package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/tool"
)

func TestPermissionGateDeny(t *testing.T) {
	et := &echoTool{}
	reg := tool.NewRegistry()
	reg.Register(et)

	ag := New(&scriptedProvider{}, reg, Options{
		Model:      "x",
		Caps:       []string{provider.CapTools},
		Permission: func(_ context.Context, _ string, _ json.RawMessage) Permission { return Deny },
	})

	h := &recHandler{}
	if err := ag.Run(context.Background(), "go", h); err != nil {
		t.Fatal(err)
	}
	if et.called != 0 {
		t.Errorf("tool ran %d times despite Deny, want 0", et.called)
	}
	// The denied call still produces a (error) tool result fed back to the model,
	// so the loop continues to a normal end_turn.
	if h.stop != provider.StopEndTurn {
		t.Errorf("stop = %q, want %q", h.stop, provider.StopEndTurn)
	}
}

func msg(role string, blocks ...provider.Block) provider.Message {
	return provider.Message{Role: role, Content: blocks}
}

func text(s string) provider.Block { return provider.Block{Type: provider.BlockText, Text: s} }

func TestWindowKeepsTaskAndIsPairingSafe(t *testing.T) {
	// history: task, assistant(tool_use), tool(result), assistant(answer)
	history := []provider.Message{
		msg("user", text("the task")),
		msg("assistant", provider.Block{Type: provider.BlockToolUse, ToolUse: &provider.ToolUseBlock{ID: "c1", Name: "echo"}}),
		msg("tool", provider.Block{Type: provider.BlockToolResult, ToolResult: &provider.ToolResultBlock{ToolUseID: "c1", Content: "r"}}),
		msg("assistant", text("answer")),
	}

	got := Window{MaxMessages: 2}.Assemble(history)

	if len(got) == 0 || got[0].Role != "user" {
		t.Fatalf("first kept message must be the user task, got %+v", got)
	}
	// No kept message may be an orphan tool_result at the suffix head.
	for i, m := range got {
		if i == 0 {
			continue
		}
		if m.Role == "tool" && (i == 1) {
			t.Errorf("suffix begins with an orphan tool_result at %d", i)
		}
	}
}

func TestWindowNoTrimWhenSmall(t *testing.T) {
	history := []provider.Message{msg("user", text("a")), msg("assistant", text("b"))}
	if got := (Window{MaxMessages: 8}).Assemble(history); len(got) != len(history) {
		t.Errorf("len = %d, want %d (no trim when under budget)", len(got), len(history))
	}
}

func TestRepairHistoryHealsPoisonedSessions(t *testing.T) {
	h := []provider.Message{
		{Role: "user", Content: []provider.Block{{Type: provider.BlockText, Text: "make this a report"}}},
		// The wedge: an assistant message whose only tool call is malformed
		// (empty name) — exactly what froze a live session.
		{Role: "assistant", Content: []provider.Block{
			{Type: provider.BlockToolUse, ToolUse: &provider.ToolUseBlock{ID: "bad", Name: ""}},
		}},
		{Role: "user", Content: []provider.Block{{Type: provider.BlockText, Text: "hello?"}}},
		// A clipped turn: a valid tool call with no tool results after it.
		{Role: "assistant", Content: []provider.Block{
			{Type: provider.BlockText, Text: "let me check"},
			{Type: provider.BlockToolUse, ToolUse: &provider.ToolUseBlock{ID: "t1", Name: "web_search", Input: map[string]any{"q": "x"}}},
		}},
	}
	got := RepairHistory(h)

	// The malformed-call message vanishes entirely (nothing valid remained).
	for _, m := range got {
		for _, b := range m.Content {
			if b.Type == provider.BlockToolUse && (b.ToolUse == nil || b.ToolUse.Name == "") {
				t.Fatal("empty-name tool_use must not survive repair")
			}
		}
	}
	// The dangling call is answered by a synthetic error result.
	last := got[len(got)-1]
	if last.Role != "tool" || len(last.Content) != 1 {
		t.Fatalf("dangling tool_use should be closed, history ends with %+v", last)
	}
	tr := last.Content[0].ToolResult
	if tr == nil || tr.ToolUseID != "t1" || !tr.IsError {
		t.Errorf("synthetic result = %+v", tr)
	}
	// Healthy histories pass through untouched.
	healthy := []provider.Message{
		{Role: "user", Content: []provider.Block{{Type: provider.BlockText, Text: "hi"}}},
		{Role: "assistant", Content: []provider.Block{{Type: provider.BlockText, Text: "hello"}}},
	}
	if out := RepairHistory(healthy); len(out) != 2 {
		t.Errorf("healthy history changed: %+v", out)
	}
}
