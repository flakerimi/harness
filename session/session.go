// Package session persists multi-turn conversations so an identity remembers
// the dialogue itself — not just durable facts (that's the memory package), but
// the running back-and-forth. A session is the agent's message history stored as
// JSON under a profile's data dir, loaded at the start of a turn and saved after,
// so `harness chat` can pick a conversation back up where it left off.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flakerimi/harness/provider"
)

// Session is one persisted conversation. Provider/Model are optional per-chat
// overrides (set e.g. by a channel's /model command); empty means "use the
// surface's default".
type Session struct {
	ID       string             `json:"id"`
	Provider string             `json:"provider,omitempty"`
	Model    string             `json:"model,omitempty"`
	History  []provider.Message `json:"history"`
}

// Turns reports how many user messages the conversation holds — a cheap proxy
// for "how long is this conversation" without counting tool round-trips.
func (s *Session) Turns() int {
	n := 0
	for _, m := range s.History {
		if m.Role == "user" {
			n++
		}
	}
	return n
}

// Store reads and writes sessions under a directory (a profile's sessions dir).
type Store struct {
	dir string
}

// NewStore builds a session store rooted at dir.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir returns the store's directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, sanitizeID(id)+".json")
}

// Load returns the session with the given id, or an empty (new) session with
// that id if none exists yet. A corrupt file is an error.
func (s *Store) Load(id string) (*Session, error) {
	raw, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return &Session{ID: id}, nil
		}
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, fmt.Errorf("session %s: %w", id, err)
	}
	if sess.ID == "" {
		sess.ID = id
	}
	return &sess, nil
}

// Save writes a session, creating the store dir as needed.
func (s *Store) Save(sess *Session) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(sess.ID), raw, 0o600)
}

// Reset clears a session's history (the conversation starts fresh) while
// keeping the id. It is a no-op if the session doesn't exist.
func (s *Store) Reset(id string) error {
	if err := os.Remove(s.path(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Meta is a lightweight session listing entry.
type Meta struct {
	ID    string
	Turns int
}

// List returns the stored sessions (sorted by id) with their turn counts.
func (s *Store) List() ([]Meta, error) {
	matches, _ := filepath.Glob(filepath.Join(s.dir, "*.json"))
	var out []Meta
	for _, p := range matches {
		id := strings.TrimSuffix(filepath.Base(p), ".json")
		sess, err := s.Load(id)
		if err != nil {
			continue // skip unreadable/corrupt files in a listing
		}
		out = append(out, Meta{ID: id, Turns: sess.Turns()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// sanitizeID keeps a session id safe as a filename.
func sanitizeID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "default"
	}
	return out
}
