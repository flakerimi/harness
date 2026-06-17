package profile

import (
	"slices"
	"testing"

	"github.com/flakerimi/harness/router"
)

func TestMeetingPrepRegistered(t *testing.T) {
	p, ok := Get("meeting-prep")
	if !ok {
		t.Fatal("meeting-prep profile not registered")
	}
	if p.BaseTier != router.TierReasoning {
		t.Errorf("BaseTier = %q, want reasoning", p.BaseTier)
	}
	if !p.Delegate || p.WorkerTier != router.TierFast {
		t.Errorf("expected delegation to a fast worker, got Delegate=%v WorkerTier=%q", p.Delegate, p.WorkerTier)
	}
	if p.Persona == "" || p.WorkerPersona == "" {
		t.Error("orchestrator and worker personas should both be set")
	}
}

func TestNamesIncludesMeetingPrep(t *testing.T) {
	if !slices.Contains(Names(), "meeting-prep") {
		t.Errorf("Names() = %v, want it to include meeting-prep", Names())
	}
}

func TestGetUnknown(t *testing.T) {
	if _, ok := Get("does-not-exist"); ok {
		t.Error("unknown profile should not resolve")
	}
}
