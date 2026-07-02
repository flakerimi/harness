package main

import (
	"strings"
	"testing"

	"github.com/flakerimi/harness/schedule"
)

func TestInstallPulseIsIdempotent(t *testing.T) {
	store := schedule.NewStore(t.TempDir())

	a, err := installPulse(store, "pulse-personal", "personal", "", "daily 08:30", "telegram:1")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "pulse-personal" || !a.Enabled || a.NextRun.IsZero() {
		t.Errorf("installed = %+v", a)
	}
	if !strings.Contains(a.Prompt, "empty pulse is a good pulse") {
		t.Errorf("pulse prompt should allow silence: %q", a.Prompt)
	}

	// Re-install with new settings replaces, never duplicates.
	b, err := installPulse(store, "pulse-personal", "personal", "claude", "every 4h", "webhook:https://example.test/hook")
	if err != nil {
		t.Fatal(err)
	}
	if b.Spec != "every 4h" || b.Deliver != "webhook:https://example.test/hook" {
		t.Errorf("reinstalled = %+v", b)
	}
	tasks, _ := store.Load()
	if len(tasks) != 1 {
		t.Fatalf("want exactly 1 pulse task, got %d", len(tasks))
	}

	// One-shot semantics do not apply: the pulse re-arms after firing.
	if schedule.Once(b.Spec) {
		t.Error("a pulse spec should recur")
	}
}
