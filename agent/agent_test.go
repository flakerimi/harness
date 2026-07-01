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
