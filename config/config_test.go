package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingIsEmpty(t *testing.T) {
	t.Setenv("HARNESS_CONFIG", filepath.Join(t.TempDir(), "nope.json"))
	c, err := Load()
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if c.Search.SearxngURL != "" {
		t.Errorf("missing file should yield empty config, got %+v", c)
	}
}

func TestLoadParses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"search":{"searxng_url":"https://x.test","searxng_token":"tok"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_CONFIG", p)

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Search.SearxngURL != "https://x.test" || c.Search.SearxngToken != "tok" {
		t.Errorf("unexpected config: %+v", c)
	}
}
