package profile

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestIdentityProfilesRegistered(t *testing.T) {
	p, ok := Get("personal")
	if !ok {
		t.Fatal("personal profile not registered")
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

func TestScopedDirsAreUnderProfile(t *testing.T) {
	// Each identity's stores live under its own data dir, so accounts/tools are
	// isolated between identities (personal vs business).
	base := DataDir("basecode")
	for name, got := range map[string]string{
		"auth":     AuthFile("basecode"),
		"memory":   MemoryDir("basecode"),
		"skills":   SkillsDir("basecode"),
		"sessions": SessionsDir("basecode"),
		"mcp":      MCPFile("basecode"),
	} {
		if !strings.HasPrefix(got, base) {
			t.Errorf("%s dir %q not under profile data dir %q", name, got, base)
		}
	}
	// Different identities never share a data dir.
	if DataDir("basecode") == DataDir("personal") {
		t.Error("distinct profiles must have distinct data dirs")
	}
}

func TestNamesIncludesIdentityProfiles(t *testing.T) {
	names := Names()
	if !slices.Contains(names, "personal") || !slices.Contains(names, "work") {
		t.Errorf("Names() = %v, want personal and work", names)
	}
}

func TestGetUnknown(t *testing.T) {
	if _, ok := Get("does-not-exist"); ok {
		t.Error("unknown profile should not resolve")
	}
}
