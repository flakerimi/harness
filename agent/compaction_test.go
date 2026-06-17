package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/tool"
)

// summarizerProvider emits a fixed summary as text — stands in for a fast-tier
// model so compaction is testable without a real backend.
type summarizerProvider struct{ calls int }

func (s *summarizerProvider) Name() string { return "sum" }

func (s *summarizerProvider) Stream(_ context.Context, _ provider.Request, emit func(provider.Event)) error {
	s.calls++
	emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "- earlier context summarized"})
	emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	return nil
}

func uMsg(text string) provider.Message {
	return provider.Message{Role: "user", Content: []provider.Block{{Type: provider.BlockText, Text: text}}}
}
func aMsg(text string) provider.Message {
	return provider.Message{Role: "assistant", Content: []provider.Block{{Type: provider.BlockText, Text: text}}}
}

func TestSplitForCompaction(t *testing.T) {
	h := []provider.Message{
		uMsg("u1"), aMsg("a1"),
		uMsg("u2"), aMsg("a2"),
		uMsg("u3"), aMsg("a3"),
	}
	prefix, tail := splitForCompaction(h, 2) // keep last 2 user-turns
	if len(prefix) != 2 || len(tail) != 4 {
		t.Fatalf("split sizes = %d/%d, want 2/4", len(prefix), len(tail))
	}
	if tail[0].Role != "user" || tail[0].Content[0].Text != "u2" {
		t.Errorf("tail should start at u2, got %+v", tail[0])
	}

	// Keeping more turns than exist → nothing to summarize.
	pre, tl := splitForCompaction(h, 9)
	if pre != nil || len(tl) != len(h) {
		t.Errorf("keep>turns should not split: %d/%d", len(pre), len(tl))
	}
}

func TestSplitTailNeverOrphansToolResult(t *testing.T) {
	h := []provider.Message{
		uMsg("u1"),
		{Role: "assistant", Content: []provider.Block{{Type: provider.BlockToolUse, ToolUse: &provider.ToolUseBlock{ID: "t", Name: "x"}}}},
		{Role: "tool", Content: []provider.Block{{Type: provider.BlockToolResult, ToolResult: &provider.ToolResultBlock{ToolUseID: "t", Content: "r"}}}},
		aMsg("a1"),
		uMsg("u2"), aMsg("a2"),
	}
	_, tail := splitForCompaction(h, 1)
	if tail[0].Role != "user" {
		t.Fatalf("tail must start on a user turn, got role %q", tail[0].Role)
	}
}

func TestEstimateTokens(t *testing.T) {
	msgs := []provider.Message{uMsg(strings.Repeat("x", 400))}
	if got := estimateTokens(msgs); got != 100 {
		t.Errorf("estimateTokens = %d, want 100", got)
	}
}

func TestAssembleCompacts(t *testing.T) {
	sp := &summarizerProvider{}
	ag := New(sp, tool.NewRegistry(), Options{CompactTokens: 50, KeepTurns: 2})

	// Build a history well over the 50-token (≈200 char) budget.
	var h []provider.Message
	for range 6 {
		h = append(h, uMsg(strings.Repeat("a", 200)), aMsg(strings.Repeat("b", 200)))
	}

	out := ag.assemble(context.Background(), h)
	if sp.calls != 1 {
		t.Fatalf("expected exactly 1 summarize call, got %d", sp.calls)
	}
	if out[0].Role != "user" || !strings.Contains(out[0].Content[0].Text, "summarized") {
		t.Errorf("first message should be the summary: %+v", out[0])
	}
	if out[1].Role != "assistant" {
		t.Errorf("second message should be the ack, got role %q", out[1].Role)
	}
	if out[2].Role != "user" {
		t.Errorf("kept tail should start on a user turn, got role %q", out[2].Role)
	}
	// The compacted prompt must be smaller than the raw history.
	if estimateTokens(out) >= estimateTokens(h) {
		t.Errorf("compaction did not shrink the prompt: %d >= %d", estimateTokens(out), estimateTokens(h))
	}

	// A second assemble over the same prefix reuses the cached summary.
	_ = ag.assemble(context.Background(), h)
	if sp.calls != 1 {
		t.Errorf("summary should be cached, got %d calls", sp.calls)
	}
}

func TestAssembleUnderBudgetIsNoop(t *testing.T) {
	sp := &summarizerProvider{}
	ag := New(sp, tool.NewRegistry(), Options{CompactTokens: 10000})
	h := []provider.Message{uMsg("short"), aMsg("reply")}
	out := ag.assemble(context.Background(), h)
	if len(out) != 2 || sp.calls != 0 {
		t.Errorf("under-budget history should pass through untouched: len=%d calls=%d", len(out), sp.calls)
	}
}

func TestAssembleDisabledByDefault(t *testing.T) {
	sp := &summarizerProvider{}
	ag := New(sp, tool.NewRegistry(), Options{}) // CompactTokens 0 → off
	var h []provider.Message
	for range 50 {
		h = append(h, uMsg(strings.Repeat("a", 200)))
	}
	out := ag.assemble(context.Background(), h)
	if len(out) != len(h) || sp.calls != 0 {
		t.Errorf("compaction must be off when CompactTokens=0: len=%d calls=%d", len(out), sp.calls)
	}
}
