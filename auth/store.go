// Package auth handles credential storage and OAuth token refresh for the
// harness. The on-disk format is compatible with Construct's providers/auth.json
// so the same file works in both: a top-level map of provider id to
// {type, credentials}. A future `login` command writes this same shape.
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Credentials is one provider's OAuth credential set.
type Credentials struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	Expires int64  `json:"expires"` // unix milliseconds
}

// entry is the stored wrapper: {"type":"oauth","credentials":{...}}.
type entry struct {
	Type        string       `json:"type"`
	Credentials *Credentials `json:"credentials"`
}

// Store reads and writes a providers/auth.json-style file. It is safe for
// concurrent use within one process.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore targets a credential file path.
func NewStore(path string) *Store { return &Store{path: path} }

// Load returns the credentials for a provider id, or an error if absent.
func (s *Store) Load(provider string) (*Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.read()
	if err != nil {
		return nil, err
	}
	e, ok := data[provider]
	if !ok || e.Credentials == nil {
		return nil, fmt.Errorf("auth: no credentials for %q in %s", provider, s.path)
	}
	return e.Credentials, nil
}

// Save writes (creating or replacing) the credentials for a provider id,
// preserving entries for other providers.
func (s *Store) Save(provider string, creds *Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.read()
	if err != nil {
		return err
	}
	data[provider] = entry{Type: "oauth", Credentials: creds}
	return s.write(data)
}

func (s *Store) read() (map[string]entry, error) {
	body, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]entry{}, nil
		}
		return nil, err
	}
	if len(body) == 0 {
		return map[string]entry{}, nil
	}
	var out map[string]entry
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("auth: parse %s: %w", s.path, err)
	}
	return out, nil
}

func (s *Store) write(data map[string]entry) error {
	body, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	// The profile's dir may not exist yet — connecting a connector is often the
	// first thing that ever writes under a fresh identity (e.g. `work`). 0700
	// because it holds credentials.
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	// Atomic-ish write: temp file + rename, restricted permissions.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
