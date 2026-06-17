// Package router decides which model handles a unit of work. Callers ask for a
// Tier (the *role* of the call — reasoning, balanced, fast); a per-provider
// Table resolves it to a concrete model. This keeps routing provider-neutral:
// "fast" means Haiku on Anthropic, gpt-4o-mini on OpenAI, deepseek-chat on
// DeepSeek. The mapping is config-backed, not hardcoded forever.
package router

import "strings"

// Tier is the role a call plays in the loop, decoupled from any model name.
type Tier string

const (
	TierFast      Tier = "fast"      // extraction, classification, summarize, sub-agents
	TierBalanced  Tier = "balanced"  // the default workhorse
	TierReasoning Tier = "reasoning" // planning, synthesis, coding — strongest
)

// ParseTier maps a free-form string (e.g. a classifier's reply) to a Tier,
// returning def when nothing matches — so ambiguity fails safe to the caller's
// default rather than silently downgrading.
func ParseTier(s string, def Tier) Tier {
	s = strings.ToLower(s)
	switch {
	case strings.Contains(s, "reason"):
		return TierReasoning
	case strings.Contains(s, "balanc"):
		return TierBalanced
	case strings.Contains(s, "fast"), strings.Contains(s, "cheap"), strings.Contains(s, "simple"):
		return TierFast
	default:
		return def
	}
}

// Escalate returns the next-stronger tier (capped at reasoning).
func Escalate(t Tier) Tier {
	switch t {
	case TierFast:
		return TierBalanced
	default:
		return TierReasoning
	}
}

// Choice is a resolved model selection.
type Choice struct {
	Model string
}

// Router resolves a (provider, tier) to a Choice. An empty Choice.Model means
// "no opinion" — the caller should fall back to its own default model.
type Router interface {
	Resolve(provider string, tier Tier) Choice
}

// Table is a config-backed Router: provider → tier → Choice.
type Table struct {
	byProvider map[string]map[Tier]Choice
}

// NewTable returns an empty Table.
func NewTable() *Table {
	return &Table{byProvider: map[string]map[Tier]Choice{}}
}

// Set assigns the model for one (provider, tier).
func (t *Table) Set(provider string, tier Tier, model string) {
	if t.byProvider[provider] == nil {
		t.byProvider[provider] = map[Tier]Choice{}
	}
	t.byProvider[provider][tier] = Choice{Model: model}
}

// Resolve looks up a (provider, tier), falling back within the provider to
// balanced, then to any defined tier.
func (t *Table) Resolve(provider string, tier Tier) Choice {
	byTier, ok := t.byProvider[provider]
	if !ok {
		return Choice{}
	}
	if c, ok := byTier[tier]; ok {
		return c
	}
	if c, ok := byTier[TierBalanced]; ok {
		return c
	}
	for _, c := range byTier {
		return c
	}
	return Choice{}
}

// DefaultTable is the built-in routing policy.
func DefaultTable() *Table {
	t := NewTable()

	// Anthropic (Provider.Name() == "anthropic").
	t.Set("anthropic", TierReasoning, "claude-opus-4-8")
	t.Set("anthropic", TierBalanced, "claude-sonnet-4-6")
	t.Set("anthropic", TierFast, "claude-haiku-4-5")

	// OpenAI (reasoning maps to an o-series model via config when desired).
	t.Set("openai", TierReasoning, "gpt-4o")
	t.Set("openai", TierBalanced, "gpt-4o")
	t.Set("openai", TierFast, "gpt-4o-mini")

	// DeepSeek — runs through the OpenAI-compatible adapter; reasoner/chat is a
	// natural reasoning/fast split within one provider.
	t.Set("deepseek", TierReasoning, "deepseek-reasoner")
	t.Set("deepseek", TierBalanced, "deepseek-chat")
	t.Set("deepseek", TierFast, "deepseek-chat")

	// Local runtimes — one model, all tiers.
	for _, slug := range []string{"ollama", "lmstudio"} {
		t.Set(slug, TierReasoning, "llama3.1")
		t.Set(slug, TierBalanced, "llama3.1")
		t.Set(slug, TierFast, "llama3.1")
	}
	return t
}
