package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/router"
	"github.com/flakerimi/harness/tool"
)

// routingProvider fumbles (empty output) on "fast-model" and succeeds otherwise,
// keyed on the requested model — to exercise escalation.
type routingProvider struct{}

func (routingProvider) Name() string { return "test" }

func (routingProvider) Stream(_ context.Context, req provider.Request, emit func(provider.Event)) error {
	if req.Model == "fast-model" {
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn}) // empty → fumbled
		return nil
	}
	emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "ok"})
	emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	return nil
}

func TestEscalateOnFumble(t *testing.T) {
	rt := router.NewTable()
	rt.Set("test", router.TierFast, "fast-model")
	rt.Set("test", router.TierBalanced, "balanced-model")

	ag := New(routingProvider{}, tool.NewRegistry(), Options{
		Router:   rt,
		BaseTier: router.TierFast,
		Escalate: true,
	})

	h := &recHandler{}
	if err := ag.Run(context.Background(), "go", h); err != nil {
		t.Fatal(err)
	}
	if h.text.String() != "ok" {
		t.Errorf("text = %q, want \"ok\" (after escalation)", h.text.String())
	}
	if len(h.routes) < 2 || h.routes[0] != "fast:fast-model" || h.routes[1] != "balanced:balanced-model" {
		t.Errorf("routes = %v, want fast then balanced", h.routes)
	}
}

// classifyProvider answers the classify call with "fast", then produces text.
type classifyProvider struct{}

func (classifyProvider) Name() string { return "test" }

func (classifyProvider) Stream(_ context.Context, req provider.Request, emit func(provider.Event)) error {
	if strings.Contains(req.System, "Classify the difficulty") {
		emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "fast"})
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
		return nil
	}
	emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "done"})
	emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	return nil
}

func TestClassifyPicksTier(t *testing.T) {
	rt := router.NewTable()
	rt.Set("test", router.TierFast, "fast-model")
	rt.Set("test", router.TierReasoning, "big-model")

	ag := New(classifyProvider{}, tool.NewRegistry(), Options{
		Router:   rt,
		BaseTier: router.TierReasoning, // classify should downgrade this
		Classify: true,
	})

	h := &recHandler{}
	if err := ag.Run(context.Background(), "what is 2+2", h); err != nil {
		t.Fatal(err)
	}
	if len(h.routes) < 1 || h.routes[0] != "fast:fast-model" {
		t.Errorf("routes = %v, want first turn fast:fast-model (classified down from reasoning)", h.routes)
	}
}

func TestModelOverrideBypassesRouting(t *testing.T) {
	rt := router.NewTable()
	rt.Set("test", router.TierFast, "fast-model")

	ag := New(routingProvider{}, tool.NewRegistry(), Options{
		Model:    "pinned-model", // explicit override — disables routing
		Router:   rt,
		Escalate: true,
	})

	h := &recHandler{}
	if err := ag.Run(context.Background(), "go", h); err != nil {
		t.Fatal(err)
	}
	// Override wins: every turn uses "pinned-model" (tier defaults to reasoning,
	// purely as a display label), and no fumble/escalation path runs.
	if len(h.routes) != 1 || h.routes[0] != "reasoning:pinned-model" {
		t.Errorf("routes = %v, want single reasoning:pinned-model", h.routes)
	}
}
