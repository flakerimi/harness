package profile

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/flakerimi/harness/router"
)

func TestFileProfileLoads(t *testing.T) {
	dir := t.TempDir()
	md := "---\nname: test-case\ndescription: A test.\nbase_tier: fast\ndelegate: true\n---\nYou are a test assistant.\n"
	if err := os.WriteFile(filepath.Join(dir, "test-case.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_PROFILES_DIR", dir)

	p, ok := Get("test-case")
	if !ok {
		t.Fatal("file profile not loaded")
	}
	if p.Persona != "You are a test assistant." {
		t.Errorf("persona = %q", p.Persona)
	}
	if p.BaseTier != router.TierFast {
		t.Errorf("base_tier = %q, want fast", p.BaseTier)
	}
	if p.Description != "A test." {
		t.Errorf("description = %q", p.Description)
	}
	if !p.Delegate || p.WorkerPersona == "" {
		t.Errorf("delegate should be on with a default worker persona, got %v / %q", p.Delegate, p.WorkerPersona)
	}
}

func TestFileProfileNameFromFilename(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "no-frontmatter.md"), []byte("Just a persona body."), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_PROFILES_DIR", dir)

	p, ok := Get("no-frontmatter")
	if !ok {
		t.Fatal("profile without frontmatter should load (name from filename)")
	}
	if p.Persona != "Just a persona body." {
		t.Errorf("persona = %q", p.Persona)
	}
}

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
