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

func TestLearnTool(t *testing.T) {
	dir := t.TempDir()
	tl := NewLearnTool(dir)

	in, _ := json.Marshal(map[string]string{
		"name":         "Weekly Report!",
		"description":  "Use to compile the Monday status report.\n(more detail)",
		"instructions": "1. Gather updates.\n2. Summarize.",
	})
	res, err := tl.Run(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "weekly-report") {
		t.Fatalf("learn result = %q err=%v", res.Content, res.IsError)
	}

	// The slug is sanitized; only the first description line is kept.
	raw, rerr := os.ReadFile(filepath.Join(dir, "weekly-report", "SKILL.md"))
	if rerr != nil {
		t.Fatalf("skill file not written: %v", rerr)
	}
	got := string(raw)
	if !strings.Contains(got, "name: weekly-report") ||
		!strings.Contains(got, "description: Use to compile the Monday status report.") ||
		strings.Contains(got, "(more detail)") ||
		!strings.Contains(got, "1. Gather updates.") {
		t.Errorf("unexpected SKILL.md:\n%s", got)
	}

	// A loaded skill should now appear and take precedence over a shared skill.
	skills, _ := Load(dir)
	var ok bool
	for _, s := range skills {
		if s.Name == "weekly-report" {
			ok = true
		}
	}
	if !ok {
		t.Error("learned skill not discoverable via Load")
	}
}

func TestLearnToolValidation(t *testing.T) {
	tl := NewLearnTool(t.TempDir())
	in, _ := json.Marshal(map[string]string{"name": "", "description": "d", "instructions": "i"})
	res, _ := tl.Run(context.Background(), in, nil)
	if !res.IsError {
		t.Error("empty name should be a validation error")
	}
}

func TestProfileSkillsWin(t *testing.T) {
	shared := t.TempDir()
	prof := t.TempDir()
	writeSkill(t, shared, "report", "shared version", "shared body")
	writeSkill(t, prof, "report", "profile version", "profile body")
	t.Setenv("HARNESS_SKILLS_DIR", shared)

	skills, _ := Load(prof) // extraDirs (profile) scanned first → wins
	var desc string
	for _, s := range skills {
		if s.Name == "report" {
			desc = s.Description
		}
	}
	if desc != "profile version" {
		t.Errorf("profile skill should win, got %q", desc)
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
