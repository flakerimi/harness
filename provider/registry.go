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
func Build(slug string) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(slug)) {
	case "", "mock":
		return NewMock(), nil
	case "anthropic", "claude":
		if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
			return NewAnthropic(key), nil
		}
		// No API key — fall back to OAuth credentials from the auth file.
		store := auth.NewStore(envOr("HARNESS_AUTH_FILE", "auth.json"))
		if _, err := store.Load("claude"); err != nil {
			return nil, fmt.Errorf("provider %q: set ANTHROPIC_API_KEY, or provide OAuth credentials: %w", slug, err)
		}
		return NewAnthropic("").WithOAuth(auth.NewAnthropicTokenSource(store, "claude")), nil
	case "openai", "chatgpt":
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("provider %q: OPENAI_API_KEY not set", slug)
		}
		return NewOpenAI("openai", envOr("OPENAI_BASE_URL", "https://api.openai.com/v1"), key), nil
	case "deepseek":
		key := os.Getenv("DEEPSEEK_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("provider %q: DEEPSEEK_API_KEY not set", slug)
		}
		return NewOpenAI("deepseek", envOr("DEEPSEEK_BASE_URL", "https://api.deepseek.com"), key), nil
	case "gemini", "google":
		key := os.Getenv("GEMINI_API_KEY")
		if key == "" {
			key = os.Getenv("GOOGLE_API_KEY")
		}
		if key == "" {
			return nil, fmt.Errorf("provider %q: GEMINI_API_KEY (or GOOGLE_API_KEY) not set", slug)
		}
		return NewOpenAI("gemini", envOr("GEMINI_BASE_URL", "https://generativelanguage.googleapis.com/v1beta/openai"), key), nil
	case "ollama":
		return NewOpenAI("ollama", envOr("OLLAMA_BASE_URL", "http://localhost:11434/v1"), ""), nil
	case "lmstudio", "lm-studio":
		return NewOpenAI("lmstudio", envOr("LMSTUDIO_BASE_URL", "http://localhost:1234/v1"), ""), nil
	default:
		return nil, fmt.Errorf("unknown provider %q (known: mock, anthropic|claude, openai, deepseek, gemini, ollama, lmstudio)", slug)
	}
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
