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

func TestNewOpenAICompatibleProviders(t *testing.T) {
	// Each should build with a key and report the expected canonical name.
	for slug, name := range map[string]string{
		"openrouter": "openrouter", "mistral": "mistral", "zai": "zai",
		"zhipu": "zai", "xai": "xai", "grok": "xai", "together": "together",
	} {
		p, err := BuildWith(slug, BuildOptions{APIKey: "k"})
		if err != nil {
			t.Errorf("BuildWith(%q) errored: %v", slug, err)
			continue
		}
		if p.Name() != name {
			t.Errorf("BuildWith(%q).Name() = %q, want %q", slug, p.Name(), name)
		}
	}
	// Missing key is a clear error, not a silent build.
	if _, err := BuildWith("mistral", BuildOptions{}); err == nil {
		t.Error("mistral without a key should error")
	}
	if DefaultModel("qwen") != "qwen-plus" || DefaultModel("zai") != "glm-4.7" {
		t.Error("qwen/zai defaults not set")
	}
}
