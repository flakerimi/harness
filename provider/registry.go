package provider

import (
	"fmt"
	"os"
	"strings"

	"github.com/flakerimi/harness/auth"
)

// Build resolves a provider slug into a concrete Provider, reading credentials
// from the environment. This is the single switch where "claude vs openai vs
// local" is decided — the agent loop above never sees it.
//
// Auth is intentionally minimal here (API keys via env). A richer TokenSource
// abstraction (OAuth / subscription profiles, per-tenant keys) slots in behind
// this function without changing any caller.
func Build(slug string) (Provider, error) { return BuildWith(slug, BuildOptions{}) }

// BuildOptions overrides credential/endpoint resolution — e.g. keys loaded from
// the config file instead of the environment. Empty fields fall back to env
// vars and then built-in defaults, so env always wins when set.
type BuildOptions struct {
	APIKey  string
	BaseURL string
}

// BuildWith resolves a provider slug into a concrete Provider, honoring explicit
// overrides before the environment. This is the single switch where "claude vs
// openai vs kimi vs local" is decided — the agent loop above never sees it.
func BuildWith(slug string, opts BuildOptions) (Provider, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))

	// key returns the override if set, else the first non-empty env var.
	key := func(envKeys ...string) string {
		if opts.APIKey != "" {
			return opts.APIKey
		}
		for _, k := range envKeys {
			if v := os.Getenv(k); v != "" {
				return v
			}
		}
		return ""
	}
	// base returns the override if set, else the env var, else the default.
	base := func(envKey, def string) string {
		if opts.BaseURL != "" {
			return opts.BaseURL
		}
		return envOr(envKey, def)
	}

	switch slug {
	case "", "mock":
		return NewMock(), nil
	case "anthropic", "claude":
		if k := key("ANTHROPIC_API_KEY"); k != "" {
			return NewAnthropic(k), nil
		}
		// No API key — fall back to OAuth credentials from the auth file.
		store := auth.NewStore(envOr("HARNESS_AUTH_FILE", "auth.json"))
		if _, err := store.Load("claude"); err != nil {
			return nil, fmt.Errorf("provider %q: set ANTHROPIC_API_KEY, or provide OAuth credentials: %w", slug, err)
		}
		return NewAnthropic("").WithOAuth(auth.NewAnthropicTokenSource(store, "claude")), nil
	case "openai", "chatgpt":
		k := key("OPENAI_API_KEY")
		if k == "" {
			return nil, errNoKey(slug, "OPENAI_API_KEY")
		}
		return NewOpenAI("openai", base("OPENAI_BASE_URL", "https://api.openai.com/v1"), k), nil
	case "deepseek":
		k := key("DEEPSEEK_API_KEY")
		if k == "" {
			return nil, errNoKey(slug, "DEEPSEEK_API_KEY")
		}
		return NewOpenAI("deepseek", base("DEEPSEEK_BASE_URL", "https://api.deepseek.com"), k), nil
	case "kimi", "moonshot":
		k := key("MOONSHOT_API_KEY", "KIMI_API_KEY")
		if k == "" {
			return nil, errNoKey(slug, "MOONSHOT_API_KEY")
		}
		return NewOpenAI("kimi", base("MOONSHOT_BASE_URL", "https://api.moonshot.ai/v1"), k), nil
	case "fireworks":
		k := key("FIREWORKS_API_KEY")
		if k == "" {
			return nil, errNoKey(slug, "FIREWORKS_API_KEY")
		}
		return NewOpenAI("fireworks", base("FIREWORKS_BASE_URL", "https://api.fireworks.ai/inference/v1"), k), nil
	case "mimo":
		k := key("MIMO_API_KEY")
		if k == "" {
			return nil, errNoKey(slug, "MIMO_API_KEY")
		}
		b := base("MIMO_BASE_URL", "")
		if b == "" {
			return nil, fmt.Errorf("provider %q: set its endpoint in config (providers.mimo.base_url) — no default is assumed", slug)
		}
		return NewOpenAI("mimo", b, k), nil
	case "gemini", "google":
		k := key("GEMINI_API_KEY", "GOOGLE_API_KEY")
		if k == "" {
			return nil, fmt.Errorf("provider %q: GEMINI_API_KEY (or GOOGLE_API_KEY) not set", slug)
		}
		return NewOpenAI("gemini", base("GEMINI_BASE_URL", "https://generativelanguage.googleapis.com/v1beta/openai"), k), nil
	case "ollama":
		return NewOpenAI("ollama", base("OLLAMA_BASE_URL", "http://localhost:11434/v1"), key()), nil
	case "lmstudio", "lm-studio":
		return NewOpenAI("lmstudio", base("LMSTUDIO_BASE_URL", "http://localhost:1234/v1"), key()), nil
	case "apple", "apple-foundation", "foundation":
		// Apple's Foundation Models are on-device (no public HTTP endpoint); this
		// talks to a local OpenAI-compatible bridge you run in front of them.
		b := base("APPLE_BASE_URL", "")
		if b == "" {
			return nil, fmt.Errorf("provider %q: Apple Foundation Models are on-device — run a local OpenAI-compatible bridge and set its endpoint (providers.apple.base_url or $APPLE_BASE_URL), plus providers.apple.model", slug)
		}
		return NewOpenAI("apple", b, key("APPLE_API_KEY")), nil
	case "qwen", "dashscope", "alibaba":
		// Qwen via its OpenAI-compatible endpoint (Alibaba DashScope). Set the
		// endpoint explicitly so no unverified URL is assumed.
		k := key("DASHSCOPE_API_KEY", "QWEN_API_KEY")
		if k == "" {
			return nil, errNoKey(slug, "DASHSCOPE_API_KEY")
		}
		b := base("QWEN_BASE_URL", "")
		if b == "" {
			return nil, fmt.Errorf("provider %q: set its OpenAI-compatible endpoint (providers.qwen.base_url or $QWEN_BASE_URL — e.g. your DashScope compatible-mode URL)", slug)
		}
		return NewOpenAI("qwen", b, k), nil
	case "openrouter":
		k := key("OPENROUTER_API_KEY")
		if k == "" {
			return nil, errNoKey(slug, "OPENROUTER_API_KEY")
		}
		return NewOpenAI("openrouter", base("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1"), k), nil
	case "mistral":
		k := key("MISTRAL_API_KEY")
		if k == "" {
			return nil, errNoKey(slug, "MISTRAL_API_KEY")
		}
		return NewOpenAI("mistral", base("MISTRAL_BASE_URL", "https://api.mistral.ai/v1"), k), nil
	case "zai", "zhipu", "glm":
		k := key("ZAI_API_KEY", "ZHIPU_API_KEY")
		if k == "" {
			return nil, errNoKey(slug, "ZAI_API_KEY")
		}
		return NewOpenAI("zai", base("ZAI_BASE_URL", "https://api.z.ai/api/paas/v4"), k), nil
	case "xai", "grok":
		k := key("XAI_API_KEY")
		if k == "" {
			return nil, errNoKey(slug, "XAI_API_KEY")
		}
		return NewOpenAI("xai", base("XAI_BASE_URL", "https://api.x.ai/v1"), k), nil
	case "together":
		k := key("TOGETHER_API_KEY")
		if k == "" {
			return nil, errNoKey(slug, "TOGETHER_API_KEY")
		}
		return NewOpenAI("together", base("TOGETHER_BASE_URL", "https://api.together.xyz/v1"), k), nil
	default:
		return nil, fmt.Errorf("unknown provider %q (known: mock, anthropic|claude, openai, deepseek, kimi|moonshot, fireworks, mimo, gemini, ollama, lmstudio, apple)", slug)
	}
}

func errNoKey(slug, env string) error {
	return fmt.Errorf("provider %q: set %s, or providers.%s.api_key in config", slug, env, slug)
}

// DefaultModel returns a reasonable default model id for a provider slug.
func DefaultModel(slug string) string {
	switch strings.ToLower(strings.TrimSpace(slug)) {
	case "anthropic", "claude":
		return "claude-opus-4-8"
	case "openai", "chatgpt":
		return "gpt-4o"
	case "deepseek":
		return "deepseek-v4-pro"
	case "kimi", "moonshot":
		return "kimi-k2.6"
	case "fireworks":
		return "accounts/fireworks/models/kimi-k2p6"
	case "mimo":
		return "mimo-v2.5-pro"
	case "gemini", "google":
		return "gemini-2.0-flash"
	case "qwen", "dashscope", "alibaba":
		return "qwen-plus"
	case "mistral":
		return "mistral-medium-2508"
	case "zai", "zhipu", "glm":
		return "glm-4.7"
	case "ollama":
		return "llama3.1"
	case "lmstudio", "lm-studio":
		return "local-model"
	case "openrouter", "xai", "grok", "together", "apple", "apple-foundation", "foundation":
		return "" // aggregator / on-device / bring-your-own — set providers.<slug>.model
	default:
		return "mock"
	}
}

// Slugs lists the canonical provider slugs Build understands (one name each) —
// for help text and validation in surfaces like the chat channel.
func Slugs() []string {
	return []string{"mock", "claude", "openai", "deepseek", "kimi", "fireworks", "mimo", "qwen", "openrouter", "mistral", "zai", "xai", "together", "gemini", "ollama", "lmstudio", "apple"}
}

// ModelInfo is one selectable model for a provider: a friendly label plus the
// id the API expects.
type ModelInfo struct {
	Label string
	ID    string
}

// Models returns a curated list of selectable models for a provider, for menus
// and pickers. An empty list means the provider is used with its default model.
func Models(slug string) []ModelInfo {
	switch strings.ToLower(strings.TrimSpace(slug)) {
	case "anthropic", "claude":
		return []ModelInfo{
			{"opus-4.8", "claude-opus-4-8"}, {"sonnet-4.6", "claude-sonnet-4-6"},
			{"haiku-4.5", "claude-haiku-4-5"}, {"fable-5", "claude-fable-5"},
		}
	case "deepseek":
		return []ModelInfo{{"v4-pro", "deepseek-v4-pro"}, {"v4-flash", "deepseek-v4-flash"}}
	case "kimi", "moonshot":
		return []ModelInfo{
			{"k2.6", "kimi-k2.6"}, {"k2.5", "kimi-k2.5"},
			{"k2.7-code", "kimi-k2.7-code"},
			{"v1-128k", "moonshot-v1-128k"}, {"v1-32k", "moonshot-v1-32k"},
		}
	case "fireworks":
		return []ModelInfo{
			{"kimi-k2p6", "accounts/fireworks/models/kimi-k2p6"},
			{"kimi-k2p5", "accounts/fireworks/models/kimi-k2p5"},
			{"deepseek-v4-pro (1M)", "accounts/fireworks/models/deepseek-v4-pro"},
			{"glm-5p2", "accounts/fireworks/models/glm-5p2"},
			{"glm-5p1", "accounts/fireworks/models/glm-5p1"},
			{"gpt-oss-120b", "accounts/fireworks/models/gpt-oss-120b"},
		}
	case "mimo":
		return []ModelInfo{{"v2.5-pro", "mimo-v2.5-pro"}, {"v2.5", "mimo-v2.5"}}
	case "qwen", "dashscope", "alibaba":
		return []ModelInfo{
			{"qwen3.7-max", "qwen3.7-max"}, {"qwen3.7-plus", "qwen3.7-plus"},
			{"qwen-flash", "qwen-flash"}, {"qwen-max", "qwen-max"}, {"qwen-plus", "qwen-plus"},
		}
	case "mistral":
		return []ModelInfo{{"medium-2508", "mistral-medium-2508"}, {"medium-2505", "mistral-medium-2505"}, {"nemo", "open-mistral-nemo"}}
	case "zai", "zhipu", "glm":
		return []ModelInfo{{"glm-4.7", "glm-4.7"}, {"glm-4.6", "glm-4.6"}, {"glm-4.5-air", "glm-4.5-air"}}
	default:
		return nil
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
