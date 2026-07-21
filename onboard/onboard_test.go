package onboard

import (
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Acme ISP":          "acme-isp",
		"  Bright/Social  ": "bright-social",
		"Globex":            "globex",
		"A & B, Inc.":       "a-b-inc",
		"!!!":               "",
		"Café 42":           "caf-42",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDraftPromptIncludesDetails(t *testing.T) {
	p := DraftPrompt(Answers{Assistant: "Morpheus", Business: "Acme ISP", Does: "fiber internet", Role: "support lead", Tone: "friendly"})
	for _, want := range []string{"Morpheus", "Acme ISP", "fiber internet", "support lead", "friendly"} {
		if !strings.Contains(p, want) {
			t.Errorf("draft prompt missing %q:\n%s", want, p)
		}
	}
}

func TestTemplatePersonaTailored(t *testing.T) {
	a := Answers{Assistant: "Morpheus", Business: "Acme ISP", Does: "regional fiber internet", Role: "support lead", Tone: "friendly, clear"}
	p := TemplatePersona(a)
	for _, want := range []string{"Morpheus", "Acme ISP", "regional fiber internet", "friendly, clear"} {
		if !strings.Contains(p, want) {
			t.Errorf("template persona missing %q", want)
		}
	}
	// The anti-leak guard must be present (never identify as a model/CLI).
	if !strings.Contains(p, "not a tool") || !strings.Contains(p, "never") {
		t.Errorf("persona missing identity guard:\n%s", p)
	}
}

func TestProfileFileStructure(t *testing.T) {
	a := Answers{Assistant: "Morpheus", Business: "Acme ISP", Does: "fiber"}
	f := ProfileFile(a, "You are Morpheus.")
	for _, want := range []string{
		"name: acme-isp",
		"description: Morpheus — assistant for Acme ISP.",
		"base_tier: reasoning",
		"delegate: true",
		"You are Morpheus.",
	} {
		if !strings.Contains(f, want) {
			t.Errorf("profile file missing %q:\n%s", want, f)
		}
	}
	if !strings.HasPrefix(f, "---\n") {
		t.Error("profile file should start with frontmatter")
	}
}
