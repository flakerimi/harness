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

	// DeepSeek — v4 pro for the hard tiers, v4 flash for cheap/fast.
	t.Set("deepseek", TierReasoning, "deepseek-v4-pro")
	t.Set("deepseek", TierBalanced, "deepseek-v4-pro")
	t.Set("deepseek", TierFast, "deepseek-v4-flash")

	// Kimi (Moonshot) — k2.6 general, k2.5 for the fast tier.
	t.Set("kimi", TierReasoning, "kimi-k2.6")
	t.Set("kimi", TierBalanced, "kimi-k2.6")
	// Not kimi-k2.5: the k2 line always thinks, so a small-budget call (the
	// tier classifier) burns its tokens on reasoning and returns nothing.
	// The v1 line answers directly; 32k leaves room for a persona prompt.
	t.Set("kimi", TierFast, "moonshot-v1-32k")

	// Fireworks — hosted open models; deepseek-v4-pro reasons, kimi for the rest.
	t.Set("fireworks", TierReasoning, "accounts/fireworks/models/deepseek-v4-pro")
	t.Set("fireworks", TierBalanced, "accounts/fireworks/models/kimi-k2p6")
	t.Set("fireworks", TierFast, "accounts/fireworks/models/kimi-k2p5")

	// MiMo (Xiaomi) — 2.5 pro for reasoning, 2.5 otherwise.
	t.Set("mimo", TierReasoning, "mimo-v2.5-pro")
	t.Set("mimo", TierBalanced, "mimo-v2.5")
	t.Set("mimo", TierFast, "mimo-v2.5")

	// Qwen (Alibaba DashScope) — 3.7 max reasons, 3.7 plus balances, flash is fast.
	t.Set("qwen", TierReasoning, "qwen3.7-max")
	t.Set("qwen", TierBalanced, "qwen3.7-plus")
	t.Set("qwen", TierFast, "qwen-flash")

	// Mistral — medium for the hard tiers, nemo for fast.
	t.Set("mistral", TierReasoning, "mistral-medium-2508")
	t.Set("mistral", TierBalanced, "mistral-medium-2508")
	t.Set("mistral", TierFast, "open-mistral-nemo")

	// Z.ai (Zhipu GLM) — 4.7 reasons, 4.6 balances, 4.5-air is cheap/fast.
	t.Set("zai", TierReasoning, "glm-4.7")
	t.Set("zai", TierBalanced, "glm-4.6")
	t.Set("zai", TierFast, "glm-4.5-air")

	// Local runtimes — one model, all tiers.
	for _, slug := range []string{"ollama", "lmstudio"} {
		t.Set(slug, TierReasoning, "llama3.1")
		t.Set(slug, TierBalanced, "llama3.1")
		t.Set(slug, TierFast, "llama3.1")
	}
	return t
}
