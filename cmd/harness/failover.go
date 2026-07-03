package main

import (
	"os"
	"strings"
)

// fallbackProviders reads $HARNESS_FALLBACK_PROVIDERS — a comma-separated
// chain of provider slugs to try when the primary fails on a transient error
// (e.g. "kimi,mistral,openrouter"). Routing re-resolves each provider's model
// for the tier, so no model ids are carried across vendors.
func fallbackProviders() []string {
	raw := os.Getenv("HARNESS_FALLBACK_PROVIDERS")
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// providerForAttempt walks the failover chain: attempt 0 runs the primary,
// attempt N runs the Nth fallback (clamped to the last).
func providerForAttempt(primary string, attempt int) string {
	if attempt <= 0 {
		return primary
	}
	fb := fallbackProviders()
	if len(fb) == 0 {
		return primary
	}
	if attempt > len(fb) {
		attempt = len(fb)
	}
	return fb[attempt-1]
}
