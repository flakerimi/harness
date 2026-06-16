package provider

import (
	"fmt"
	"os"
	"strings"
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
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("provider %q: ANTHROPIC_API_KEY not set", slug)
		}
		return NewAnthropic(key), nil
	case "openai", "chatgpt":
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("provider %q: OPENAI_API_KEY not set", slug)
		}
		return NewOpenAI("openai", envOr("OPENAI_BASE_URL", "https://api.openai.com/v1"), key), nil
	case "ollama":
		return NewOpenAI("ollama", envOr("OLLAMA_BASE_URL", "http://localhost:11434/v1"), ""), nil
	case "lmstudio", "lm-studio":
		return NewOpenAI("lmstudio", envOr("LMSTUDIO_BASE_URL", "http://localhost:1234/v1"), ""), nil
	default:
		return nil, fmt.Errorf("unknown provider %q (known: mock, anthropic|claude, openai, ollama, lmstudio)", slug)
	}
}

// DefaultModel returns a reasonable default model id for a provider slug.
func DefaultModel(slug string) string {
	switch strings.ToLower(strings.TrimSpace(slug)) {
	case "anthropic", "claude":
		return "claude-opus-4-8"
	case "openai", "chatgpt":
		return "gpt-4o"
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
