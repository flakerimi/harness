package subagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSpec(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAndDiscovery(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "researcher", "---\nname: researcher\ndescription: Research things.\ntier: fast\ntools: web_search, web_fetch\n---\nYou research.")
	t.Setenv("HARNESS_AGENTS_DIR", dir)

	specs, errs := Load()
	if len(errs) != 0 {
		t.Fatalf("load errors: %v", errs)
	}
	var r *Spec
	for i := range specs {
		if specs[i].Name == "researcher" {
			r = &specs[i]
		}
	}
	if r == nil {
		t.Fatal("researcher spec not loaded")
	}
	if r.Tier != "fast" || r.Persona != "You research." {
		t.Errorf("unexpected spec: %+v", *r)
	}
	if len(r.Tools) != 2 || r.Tools[0] != "web_search" || r.Tools[1] != "web_fetch" {
		t.Errorf("tools not parsed: %v", r.Tools)
	}

	d := DiscoveryText(specs)
	if !strings.Contains(d, "researcher") || !strings.Contains(d, "dispatch") {
		t.Errorf("discovery text missing pieces: %q", d)
	}
	if DiscoveryText(nil) != "" {
		t.Error("no specs should yield empty discovery text")
	}
}

func TestProfileSpecsWin(t *testing.T) {
	shared := t.TempDir()
	prof := t.TempDir()
	writeSpec(t, shared, "x", "---\nname: x\ndescription: shared.\n---\nshared")
	writeSpec(t, prof, "x", "---\nname: x\ndescription: profile.\n---\nprofile")
	t.Setenv("HARNESS_AGENTS_DIR", shared)

	specs, _ := Load(prof) // extraDirs first → wins
	for _, s := range specs {
		if s.Name == "x" && s.Description != "profile." {
			t.Errorf("profile spec should win, got %q", s.Description)
		}
	}
}

func TestMissingDescriptionErrors(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "bad", "---\nname: bad\n---\nno description")
	_, errs := Load(dir)
	if len(errs) == 0 {
		t.Error("a spec without a description should error")
	}
}
