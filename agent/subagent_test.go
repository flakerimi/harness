package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/router"
	"github.com/flakerimi/harness/tool"
)

// textProvider streams a fixed string.
type textProvider struct{ text string }

func (textProvider) Name() string { return "test" }

func (p textProvider) Stream(_ context.Context, _ provider.Request, emit func(provider.Event)) error {
	emit(provider.Event{Type: provider.EventTextDelta, TextDelta: p.text})
	emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	return nil
}

func TestDelegateRunsWorker(t *testing.T) {
	rt := router.NewTable()
	rt.Set("test", router.TierFast, "fast-model")

	d := Delegate{
		Provider: textProvider{text: "DOSSIER: Jane Doe is CEO of Acme."},
		Tools:    tool.NewRegistry(),
		Router:   rt,
		Tier:     router.TierFast,
		System:   "research",
	}

	in, _ := json.Marshal(map[string]string{"task": "research Jane Doe at Acme"})
	res, err := d.Run(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "DOSSIER: Jane Doe is CEO of Acme.") {
		t.Fatalf("delegate result = %q isErr=%v", res.Content, res.IsError)
	}
}

func TestDelegateRequiresTask(t *testing.T) {
	d := Delegate{Provider: textProvider{}, Tools: tool.NewRegistry(), Router: router.NewTable(), Tier: router.TierFast}
	in, _ := json.Marshal(map[string]string{"task": "  "})
	res, _ := d.Run(context.Background(), in, nil)
	if !res.IsError {
		t.Error("empty task should be an error result")
	}
}
