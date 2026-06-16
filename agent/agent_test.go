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
}

func (r *recHandler) OnText(d string)                      { r.text.WriteString(d) }
func (r *recHandler) OnToolStart(_, _ string)              { r.toolStarts++ }
func (r *recHandler) OnToolResult(_ string, _ tool.Result) { r.toolResults++ }
func (r *recHandler) OnUsage(_ provider.Usage)             {}
func (r *recHandler) OnStop(reason string)                 { r.stop = reason }

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
