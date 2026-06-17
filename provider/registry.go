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
	default:
		return nil, fmt.Errorf("unknown provider %q (known: mock, anthropic|claude, openai, deepseek, kimi|moonshot, fireworks, mimo, gemini, ollama, lmstudio)", slug)
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
		return "deepseek-chat"
	case "kimi", "moonshot":
		return "moonshot-v1-8k"
	case "gemini", "google":
		return "gemini-2.0-flash"
	case "ollama":
		return "llama3.1"
	case "lmstudio", "lm-studio":
		return "local-model"
	default:
		return "mock"
	}
}

// Slugs lists the canonical provider slugs Build understands (one name each) —
// for help text and validation in surfaces like the chat channel.
func Slugs() []string {
	return []string{"mock", "claude", "openai", "deepseek", "kimi", "fireworks", "mimo", "gemini", "ollama", "lmstudio"}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
