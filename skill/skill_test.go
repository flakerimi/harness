package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, name, desc, body string) {
	t.Helper()
	d := filepath.Join(root, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAndLoadTool(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "haiku", "Use for haiku.", "Write in 5-7-5.")
	t.Setenv("HARNESS_SKILLS_DIR", dir)

	skills, errs := Load()
	if len(errs) != 0 {
		t.Fatalf("unexpected load errors: %v", errs)
	}
	var found *Skill
	for i := range skills {
		if skills[i].Name == "haiku" {
			found = &skills[i]
		}
	}
	if found == nil {
		t.Fatal("haiku skill not loaded")
	}
	if found.Description != "Use for haiku." || found.Body != "Write in 5-7-5." {
		t.Errorf("unexpected skill: %+v", *found)
	}

	tl := NewLoadTool(skills)
	in, _ := json.Marshal(map[string]string{"name": "haiku"})
	res, _ := tl.Run(context.Background(), in, nil)
	if res.IsError || res.Content != "Write in 5-7-5." {
		t.Errorf("load_skill returned %q err=%v", res.Content, res.IsError)
	}

	bad, _ := json.Marshal(map[string]string{"name": "nope"})
	res2, _ := tl.Run(context.Background(), bad, nil)
	if !res2.IsError {
		t.Error("unknown skill should be an error result")
	}
}

func TestDiscoveryText(t *testing.T) {
	skills := []Skill{{Name: "haiku", Description: "Use for haiku."}}
	d := DiscoveryText(skills)
	if !strings.Contains(d, "haiku") || !strings.Contains(d, "Use for haiku.") || !strings.Contains(d, "load_skill") {
		t.Errorf("discovery text missing pieces: %q", d)
	}
	if DiscoveryText(nil) != "" {
		t.Error("no skills should yield empty discovery text")
	}
}
