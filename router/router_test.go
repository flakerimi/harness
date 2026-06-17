package router

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultTableResolve(t *testing.T) {
	tbl := DefaultTable()
	cases := []struct {
		provider string
		tier     Tier
		want     string
	}{
		{"anthropic", TierReasoning, "claude-opus-4-8"},
		{"anthropic", TierFast, "claude-haiku-4-5"},
		{"openai", TierFast, "gpt-4o-mini"},
		{"deepseek", TierReasoning, "deepseek-reasoner"},
		{"deepseek", TierFast, "deepseek-chat"},
	}
	for _, c := range cases {
		if got := tbl.Resolve(c.provider, c.tier).Model; got != c.want {
			t.Errorf("Resolve(%q,%q) = %q, want %q", c.provider, c.tier, got, c.want)
		}
	}
	if got := tbl.Resolve("unknown", TierFast).Model; got != "" {
		t.Errorf("unknown provider should resolve empty, got %q", got)
	}
}

func TestParseTier(t *testing.T) {
	if ParseTier("reasoning, definitely", TierFast) != TierReasoning {
		t.Error("should parse reasoning")
	}
	if ParseTier("Fast", TierBalanced) != TierFast {
		t.Error("should parse fast (case-insensitive)")
	}
	if ParseTier("gibberish", TierBalanced) != TierBalanced {
		t.Error("unmatched should fall back to def")
	}
}

func TestEscalate(t *testing.T) {
	if Escalate(TierFast) != TierBalanced {
		t.Error("fast → balanced")
	}
	if Escalate(TierBalanced) != TierReasoning {
		t.Error("balanced → reasoning")
	}
	if Escalate(TierReasoning) != TierReasoning {
		t.Error("reasoning is the ceiling")
	}
}

func TestLoadTableOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	body := `{"providers":{"anthropic":{"fast":"custom-haiku"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	tbl, err := LoadTable(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.Resolve("anthropic", TierFast).Model; got != "custom-haiku" {
		t.Errorf("override not applied: got %q", got)
	}
	if got := tbl.Resolve("anthropic", TierReasoning).Model; got != "claude-opus-4-8" {
		t.Errorf("non-overridden tier should keep default, got %q", got)
	}
}

func TestLoadTableMissingIsDefault(t *testing.T) {
	tbl, err := LoadTable(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if tbl.Resolve("anthropic", TierFast).Model != "claude-haiku-4-5" {
		t.Error("missing file should yield the default table")
	}
}
