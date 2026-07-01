package provider

import (
	"strings"
	"testing"
)

func TestBuildWithNewProviders(t *testing.T) {
	cases := []struct {
		slug, wantName string
		opts           BuildOptions
	}{
		{"kimi", "kimi", BuildOptions{APIKey: "k"}},
		{"moonshot", "kimi", BuildOptions{APIKey: "k"}},
		{"fireworks", "fireworks", BuildOptions{APIKey: "fw"}},
		{"deepseek", "deepseek", BuildOptions{APIKey: "sk"}},
		{"mimo", "mimo", BuildOptions{APIKey: "k", BaseURL: "https://example.test/v1"}},
	}
	for _, c := range cases {
		p, err := BuildWith(c.slug, c.opts)
		if err != nil {
			t.Errorf("BuildWith(%q): %v", c.slug, err)
			continue
		}
		if p.Name() != c.wantName {
			t.Errorf("BuildWith(%q).Name() = %q, want %q", c.slug, p.Name(), c.wantName)
		}
	}
}

func TestBuildWithMissingKey(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "")
	if _, err := BuildWith("fireworks", BuildOptions{}); err == nil {
		t.Error("fireworks without a key should error")
	} else if !strings.Contains(err.Error(), "providers.fireworks.api_key") {
		t.Errorf("error should point to config: %v", err)
	}
}

func TestBuildWithMimoNeedsBaseURL(t *testing.T) {
	_, err := BuildWith("mimo", BuildOptions{APIKey: "k"})
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Errorf("mimo without base_url should error about base_url, got %v", err)
	}
}

func TestDefaultModelKimi(t *testing.T) {
	if DefaultModel("kimi") != "kimi-k2.6" {
		t.Errorf("DefaultModel(kimi) = %q", DefaultModel("kimi"))
	}
}

func TestBuildWithAppleNeedsBaseURL(t *testing.T) {
	if _, err := BuildWith("apple", BuildOptions{}); err == nil {
		t.Error("apple without a base_url should error (on-device, needs a bridge)")
	}
}
