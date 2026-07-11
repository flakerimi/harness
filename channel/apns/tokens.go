package apns

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Token is one registered device.
type Token struct {
	Token    string    `json:"token"`
	Platform string    `json:"platform,omitempty"` // "ios" for now
	Added    time.Time `json:"added"`
}

// TokenStore persists device tokens for one identity as a JSON file in its
// data dir — the same dropped-file convention as everything else pluggable.
// Registration is idempotent; delivery prunes tokens APNs reports dead.
type TokenStore struct{ path string }

// NewTokenStore stores tokens under dir/push-tokens.json.
func NewTokenStore(dir string) *TokenStore {
	return &TokenStore{path: filepath.Join(dir, "push-tokens.json")}
}

func (s *TokenStore) load() []Token {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var out []Token
	_ = json.Unmarshal(raw, &out)
	return out
}

func (s *TokenStore) save(list []Token) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0o600)
}

// Add registers a device token, replacing an existing entry in place.
func (s *TokenStore) Add(token, platform string) error {
	list := s.load()
	for i, t := range list {
		if t.Token == token {
			list[i].Platform = platform
			return s.save(list)
		}
	}
	return s.save(append(list, Token{Token: token, Platform: platform, Added: time.Now()}))
}

// List returns all registered tokens.
func (s *TokenStore) List() []Token { return s.load() }

// Remove drops a token — delivery calls this when APNs says Unregistered.
func (s *TokenStore) Remove(token string) error {
	list := s.load()
	kept := list[:0]
	for _, t := range list {
		if t.Token != token {
			kept = append(kept, t)
		}
	}
	return s.save(kept)
}
