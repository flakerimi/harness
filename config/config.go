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
}

// SearchConfig configures the web_search backend.
type SearchConfig struct {
	SearxngURL   string `json:"searxng_url"`   // may embed basic-auth creds: https://user:pass@host
	SearxngToken string `json:"searxng_token"` // optional bearer token
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
