package main

import "testing"

func TestProviderForAttemptWalksTheChain(t *testing.T) {
	t.Setenv("HARNESS_FALLBACK_PROVIDERS", "kimi, mistral,openrouter")
	cases := map[int]string{0: "deepseek", 1: "kimi", 2: "mistral", 3: "openrouter", 9: "openrouter"}
	for attempt, want := range cases {
		if got := providerForAttempt("deepseek", attempt); got != want {
			t.Errorf("attempt %d = %q, want %q", attempt, got, want)
		}
	}

	// No chain configured → always the primary.
	t.Setenv("HARNESS_FALLBACK_PROVIDERS", "")
	if got := providerForAttempt("deepseek", 2); got != "deepseek" {
		t.Errorf("no fallbacks: got %q", got)
	}
}

func TestIsTransientErrClassification(t *testing.T) {
	transient := []string{
		`openai: request: Post "https://api.deepseek.com/chat/completions": dial tcp 3.173.21.63:443: i/o timeout`,
		"HTTP 503: upstream overloaded",
		"HTTP 429: rate limited",
		"read: connection reset by peer",
	}
	for _, s := range transient {
		if !isTransientErr(errString(s)) {
			t.Errorf("%q should be transient", s)
		}
	}
	permanent := []string{"HTTP 401: invalid api key", "HTTP 400: model not found", "agent: exceeded 48 turns"}
	for _, s := range permanent {
		if isTransientErr(errString(s)) {
			t.Errorf("%q must NOT be transient", s)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
