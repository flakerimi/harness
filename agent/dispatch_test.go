package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/tool"
)

// echoTextProvider streams back a fixed line — enough to exercise dispatch.
type echoTextProvider struct{}

func (echoTextProvider) Name() string { return "echo" }
func (echoTextProvider) Stream(_ context.Context, _ provider.Request, emit func(provider.Event)) error {
	emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "specialist did the work"})
	emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	return nil
}

func TestDispatchRoutesToNamedSpecialist(t *testing.T) {
	d := NewDispatch(echoTextProvider{}, nil, 256, nil, map[string]WorkerConfig{
		"researcher": {Description: "research", Tier: "fast", System: "you research", Tools: tool.NewRegistry()},
	})

	// The schema advertises the roster as an enum.
	spec := d.Spec()
	props := spec.InputSchema["properties"].(map[string]any)
	enum := props["agent"].(map[string]any)["enum"].([]string)
	if len(enum) != 1 || enum[0] != "researcher" {
		t.Errorf("roster enum = %v, want [researcher]", enum)
	}

	in, _ := json.Marshal(map[string]string{"agent": "researcher", "task": "look into X"})
	res, err := d.Run(context.Background(), in, &tool.Env{Root: "."})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "specialist did the work") {
		t.Errorf("dispatch result = %q err=%v", res.Content, res.IsError)
	}
}

func TestDispatchUnknownSpecialist(t *testing.T) {
	d := NewDispatch(echoTextProvider{}, nil, 256, nil, map[string]WorkerConfig{
		"researcher": {Tools: tool.NewRegistry()},
	})
	in, _ := json.Marshal(map[string]string{"agent": "nope", "task": "x"})
	res, _ := d.Run(context.Background(), in, &tool.Env{Root: "."})
	if !res.IsError || !strings.Contains(res.Content, "unknown specialist") {
		t.Errorf("unknown specialist should error: %q", res.Content)
	}
}

func TestDispatchRequiresTask(t *testing.T) {
	d := NewDispatch(echoTextProvider{}, nil, 256, nil, map[string]WorkerConfig{"r": {Tools: tool.NewRegistry()}})
	in, _ := json.Marshal(map[string]string{"agent": "r", "task": ""})
	res, _ := d.Run(context.Background(), in, &tool.Env{Root: "."})
	if !res.IsError {
		t.Error("empty task should be a validation error")
	}
}
