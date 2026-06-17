// Package config loads the harness's user-level settings file so things like
// the search backend don't have to be passed on every invocation. Environment
// variables still override file values, and a missing file is not an error.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the harness settings file (JSON).
type Config struct {
	Search SearchConfig `json:"search"`
	Google GoogleConfig `json:"google"`

	// DefaultProfile is the identity profile used when -profile is not passed
	// (e.g. "personal"). Env HARNESS_PROFILE overrides.
	DefaultProfile string `json:"default_profile"`

	// Providers stores model-provider credentials/endpoints by slug, so API keys
	// don't have to live in the environment. Env vars still override at build time.
	Providers map[string]ProviderConfig `json:"providers,omitempty"`
}

// ProviderConfig holds a model provider's stored credential and optional
// endpoint/model overrides (for OpenAI-compatible providers).
type ProviderConfig struct {
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
	Model   string `json:"model,omitempty"`
}

// ProviderConf returns the stored config for a provider slug (zero value if
// unset).
func (c Config) ProviderConf(slug string) ProviderConfig {
	return c.Providers[slug]
}

// Profile resolves the default identity profile: HARNESS_PROFILE env, else the
// config value.
func (c Config) Profile() string {
	if v := os.Getenv("HARNESS_PROFILE"); v != "" {
		return v
	}
	return c.DefaultProfile
}

// SearchConfig configures the web_search backend.
type SearchConfig struct {
	SearxngURL   string `json:"searxng_url"`   // may embed basic-auth creds: https://user:pass@host
	SearxngToken string `json:"searxng_token"` // optional bearer token
}

// GoogleConfig holds the Google OAuth client (a "Desktop app" client from
// console.cloud.google.com). Env GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET override.
type GoogleConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// GoogleClient resolves the Google OAuth client id/secret, env overriding file.
func (c Config) GoogleClient() (id, secret string) {
	id, secret = c.Google.ClientID, c.Google.ClientSecret
	if v := os.Getenv("GOOGLE_CLIENT_ID"); v != "" {
		id = v
	}
	if v := os.Getenv("GOOGLE_CLIENT_SECRET"); v != "" {
		secret = v
	}
	return id, secret
}

// Path returns the config file location: $HARNESS_CONFIG, else
// <user-config-dir>/harness/config.json.
func Path() string {
	if v := os.Getenv("HARNESS_CONFIG"); v != "" {
		return v
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "harness.json"
	}
	return filepath.Join(dir, "harness", "config.json")
}

// Save writes the config back to its file (creating the dir), preserving every
// field — so callers can load, tweak one thing, and save without clobbering the
// rest. Written at 0600 since it holds API keys.
func Save(c Config) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// SetDefaultProfile loads the config, sets the default identity, and saves.
func SetDefaultProfile(name string) error {
	c, err := Load()
	if err != nil {
		return err
	}
	c.DefaultProfile = name
	return Save(c)
}

// Load reads the config file. A missing file yields a zero Config and no error.
func Load() (Config, error) {
	var c Config
	body, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(body, &c); err != nil {
		return c, fmt.Errorf("config %s: %w", Path(), err)
	}
	return c, nil
}
