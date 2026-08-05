package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/tool"
)

// scriptedProvider emits one tool call on the first turn, then text on the
// second — exercising the full call → result → answer loop.
type scriptedProvider struct{ turn int }

func (s *scriptedProvider) Name() string { return "scripted" }

func (s *scriptedProvider) Stream(_ context.Context, _ provider.Request, emit func(provider.Event)) error {
	defer func() { s.turn++ }()
	if s.turn == 0 {
		emit(provider.Event{Type: provider.EventToolUseStart, Index: 0, ToolUseID: "c1", ToolName: "echo"})
		emit(provider.Event{Type: provider.EventToolUseDelta, Index: 0, InputDelta: `{"msg":"hi"}`})
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopToolUse})
		return nil
	}
	emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "done"})
	emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	return nil
}

// parallelProvider emits two tool calls on different indices in one turn.
type parallelProvider struct{ turn int }

func (p *parallelProvider) Name() string { return "parallel" }

func (p *parallelProvider) Stream(_ context.Context, _ provider.Request, emit func(provider.Event)) error {
	defer func() { p.turn++ }()
	if p.turn == 0 {
		emit(provider.Event{Type: provider.EventToolUseStart, Index: 0, ToolUseID: "c0", ToolName: "echo"})
		emit(provider.Event{Type: provider.EventToolUseDelta, Index: 0, InputDelta: `{"n":0}`})
		emit(provider.Event{Type: provider.EventToolUseStart, Index: 1, ToolUseID: "c1", ToolName: "echo"})
		emit(provider.Event{Type: provider.EventToolUseDelta, Index: 1, InputDelta: `{"n":1}`})
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopToolUse})
		return nil
	}
	emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "ok"})
	emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	return nil
}

// foreignIDProvider mimics an OpenAI-compatible vendor whose tool-call ids
// carry characters Anthropic rejects on replay — plus one lost entirely.
type foreignIDProvider struct{ turn int }

func (f *foreignIDProvider) Name() string { return "foreign" }

func (f *foreignIDProvider) Stream(_ context.Context, _ provider.Request, emit func(provider.Event)) error {
	defer func() { f.turn++ }()
	if f.turn == 0 {
		emit(provider.Event{Type: provider.EventToolUseStart, Index: 0, ToolUseID: "functions.echo:0", ToolName: "echo"})
		emit(provider.Event{Type: provider.EventToolUseDelta, Index: 0, InputDelta: `{"n":0}`})
		emit(provider.Event{Type: provider.EventToolUseStart, Index: 1, ToolUseID: "", ToolName: "echo"})
		emit(provider.Event{Type: provider.EventToolUseDelta, Index: 1, InputDelta: `{"n":1}`})
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopToolUse})
		return nil
	}
	emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "ok"})
	emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	return nil
}

// The loop must never persist a vendor tool id that another provider would
// reject on replay — histories are provider-neutral, ids included.
func TestLoopPersistsOnlyPortableToolIDs(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&echoTool{})
	ag := New(&foreignIDProvider{}, reg, Options{})

	history, err := ag.Continue(context.Background(), nil, "go", &recHandler{})
	if err != nil {
		t.Fatal(err)
	}

	var ids []string
	for _, m := range history {
		for _, b := range m.Content {
			switch {
			case b.Type == provider.BlockToolUse && b.ToolUse != nil:
				if !portableToolIDRe.MatchString(b.ToolUse.ID) {
					t.Errorf("persisted tool_use id %q not portable", b.ToolUse.ID)
				}
				ids = append(ids, b.ToolUse.ID)
			case b.Type == provider.BlockToolResult && b.ToolResult != nil:
				if !portableToolIDRe.MatchString(b.ToolResult.ToolUseID) {
					t.Errorf("persisted tool_result id %q not portable", b.ToolResult.ToolUseID)
				}
			}
		}
	}
	if len(ids) == 2 && ids[0] == ids[1] {
		t.Error("two tool calls persisted with the same id")
	}
}

type echoTool struct{ called int }

func (e *echoTool) Spec() tool.Spec {
	return tool.Spec{Name: "echo", Description: "echo", InputSchema: map[string]any{"type": "object"}}
}

func (e *echoTool) Run(_ context.Context, input json.RawMessage, _ *tool.Env) (tool.Result, error) {
	e.called++
	return tool.Result{Content: "echoed: " + string(input)}, nil
}

type recHandler struct {
	text        strings.Builder
	toolStarts  int
	toolResults int
	stop        string
	routes      []string // "tier:model" per turn
	critiqued   bool
}

func (r *recHandler) OnText(d string)                      { r.text.WriteString(d) }
func (r *recHandler) OnToolStart(_, _ string)              { r.toolStarts++ }
func (r *recHandler) OnToolResult(_ string, _ tool.Result) { r.toolResults++ }
func (r *recHandler) OnUsage(_ provider.Usage)             {}
func (r *recHandler) OnStop(reason string)                 { r.stop = reason }
func (r *recHandler) OnRoute(tier, model string)           { r.routes = append(r.routes, tier+":"+model) }
func (r *recHandler) OnCritique(_ int, _ string)           { r.critiqued = true }

func TestAgentToolLoop(t *testing.T) {
	et := &echoTool{}
	reg := tool.NewRegistry()
	reg.Register(et)
	ag := New(&scriptedProvider{}, reg, Options{Model: "x", Caps: []string{provider.CapTools}})

	h := &recHandler{}
	if err := ag.Run(context.Background(), "go", h); err != nil {
		t.Fatal(err)
	}

	if et.called != 1 {
		t.Errorf("tool called %d times, want 1", et.called)
	}
	if h.toolStarts != 1 || h.toolResults != 1 {
		t.Errorf("tool starts=%d results=%d, want 1/1", h.toolStarts, h.toolResults)
	}
	if h.stop != provider.StopEndTurn {
		t.Errorf("stop = %q, want %q", h.stop, provider.StopEndTurn)
	}
	if got := h.text.String(); got != "done" {
		t.Errorf("final text = %q, want %q", got, "done")
	}
}

// critiqueProvider scripts a critique loop: turn 0 is a weak answer, turn 1 is
// the critic (finds a problem), turn 2 is the revised answer, turn 3 is the
// critic again (approves). The agent's critique() call and the answer turns
// share this provider, so odd turns are critic calls.
type critiqueProvider struct{ turn int }

func (p *critiqueProvider) Name() string { return "critique" }

func (p *critiqueProvider) Stream(_ context.Context, req provider.Request, emit func(provider.Event)) error {
	defer func() { p.turn++ }()
	text := func(s string) {
		emit(provider.Event{Type: provider.EventTextDelta, TextDelta: s})
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	}
	// A critic call is the one whose system prompt asks for a review.
	isCritic := strings.Contains(req.System, "strict reviewer")
	switch {
	case !isCritic && p.turn == 0:
		text("weak answer")
	case isCritic && p.turn == 1:
		text("- missing the actual result")
	case !isCritic && p.turn == 2:
		text("revised answer with the result")
	default:
		text("OK")
	}
	return nil
}

func TestAgentCritiqueLoop(t *testing.T) {
	reg := tool.NewRegistry()
	ag := New(&critiqueProvider{}, reg, Options{Model: "x", Critique: true})

	h := &recHandler{}
	msgs, err := ag.Continue(context.Background(), nil, "do the task", h)
	if err != nil {
		t.Fatal(err)
	}
	// The returned answer must be the revised one, not the weak first draft.
	last := msgs[len(msgs)-1]
	if got := messageText(last); !strings.Contains(got, "revised") {
		t.Errorf("final answer = %q, want the revised one", got)
	}
	if !h.critiqued {
		t.Error("handler should have been notified of the critique pass")
	}
}

func TestAgentCritiqueApprovesGoodAnswer(t *testing.T) {
	// A provider whose answer passes review on the first check → no revision.
	reg := tool.NewRegistry()
	ag := New(&okProvider{}, reg, Options{Model: "x", Critique: true})
	h := &recHandler{}
	if err := ag.Run(context.Background(), "task", h); err != nil {
		t.Fatal(err)
	}
	if h.critiqued {
		t.Error("a passing answer should not trigger a revision")
	}
	if got := h.text.String(); got != "good answer" {
		t.Errorf("final text = %q, want the original (unrevised)", got)
	}
}

// okProvider answers once, and its critic call always approves.
type okProvider struct{}

func (okProvider) Name() string { return "ok" }
func (okProvider) Stream(_ context.Context, req provider.Request, emit func(provider.Event)) error {
	if strings.Contains(req.System, "strict reviewer") {
		emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "OK"})
	} else {
		emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "good answer"})
	}
	emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	return nil
}

func TestAgentParallelTools(t *testing.T) {
	et := &echoTool{}
	reg := tool.NewRegistry()
	reg.Register(et)
	ag := New(&parallelProvider{}, reg, Options{Model: "x", Caps: []string{provider.CapTools}})

	h := &recHandler{}
	if err := ag.Run(context.Background(), "go", h); err != nil {
		t.Fatal(err)
	}

	if et.called != 2 {
		t.Errorf("tool called %d times, want 2 (both parallel calls dispatched)", et.called)
	}
	if h.stop != provider.StopEndTurn {
		t.Errorf("stop = %q, want %q", h.stop, provider.StopEndTurn)
	}
}

// loopingProvider calls a tool every turn until it sees the wrap-up nudge
// (a request without tools), then answers with text — the budget-exhaustion path.
type loopingProvider struct{ turn int }

func (l *loopingProvider) Name() string { return "looping" }

func (l *loopingProvider) Stream(_ context.Context, req provider.Request, emit func(provider.Event)) error {
	defer func() { l.turn++ }()
	if len(req.Tools) == 0 {
		emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "wrap-up: best effort from partial work"})
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
		return nil
	}
	emit(provider.Event{Type: provider.EventToolUseStart, Index: 0, ToolUseID: "x", ToolName: "echo"})
	emit(provider.Event{Type: provider.EventToolUseDelta, Index: 0, InputDelta: `{"msg":"again"}`})
	emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopToolUse})
	return nil
}

func TestTurnBudgetWrapsUpInsteadOfErroring(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&echoTool{})
	prov := &loopingProvider{}
	ag := New(prov, reg, Options{MaxTurns: 3})

	var out strings.Builder
	history, err := ag.Continue(context.Background(), nil, "go", &collectHandler{out: &out})
	if err != nil {
		t.Fatalf("budget exhaustion should wrap up, not error: %v", err)
	}
	if !strings.Contains(out.String(), "wrap-up") {
		t.Errorf("final text = %q, want the wrap-up answer", out.String())
	}
	// 3 tool turns + 1 final no-tools turn.
	if prov.turn != 4 {
		t.Errorf("provider turns = %d, want 4 (3 budget + 1 wrap-up)", prov.turn)
	}
	// The wrap-up nudge and answer are part of the returned history.
	last := history[len(history)-1]
	if last.Role != "assistant" {
		t.Errorf("history should end with the wrap-up answer, ends with %q", last.Role)
	}
}

// clippedProvider simulates an output-token limit cutting a tool call's
// arguments: turn 1 emits a truncated tool call with stop max_tokens; after
// receiving the error result, turn 2 answers with text.
type clippedProvider struct{ turn int }

func (c *clippedProvider) Name() string { return "clipped" }

func (c *clippedProvider) Stream(_ context.Context, req provider.Request, emit func(provider.Event)) error {
	defer func() { c.turn++ }()
	if c.turn == 0 {
		emit(provider.Event{Type: provider.EventToolUseStart, Index: 0, ToolUseID: "w1", ToolName: "echo"})
		emit(provider.Event{Type: provider.EventToolUseDelta, Index: 0, InputDelta: `{"msg":"a huge unfinished...`})
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopMaxTokens})
		return nil
	}
	// The model must have been told its call was truncated.
	last := req.Messages[len(req.Messages)-1]
	for _, b := range last.Content {
		if b.ToolResult != nil && b.ToolResult.IsError && strings.Contains(b.ToolResult.Content, "truncated") {
			emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "adapted: writing in sections"})
			emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
			return nil
		}
	}
	emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "never told about truncation"})
	emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	return nil
}

func TestTruncatedToolCallGetsFeedbackAndLoopContinues(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&echoTool{})
	var out strings.Builder
	_, err := New(&clippedProvider{}, reg, Options{}).Continue(context.Background(), nil, "write it", &collectHandler{out: &out})
	if err != nil {
		t.Fatalf("clipped tool call should recover, got %v", err)
	}
	if !strings.Contains(out.String(), "adapted") {
		t.Errorf("model was not fed the truncation error: %q", out.String())
	}
}
