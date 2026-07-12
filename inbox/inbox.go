// Package inbox is the durable delivery feed for an identity's own app: where
// scheduled runs and background tasks land so they outlive the push banner
// that announces them. Telegram chats used to be this archive; retiring that
// surface means the harness must keep the record itself. One JSON file per
// identity, newest last, capped so it never grows without bound.
package inbox

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Item is one delivered message.
type Item struct {
	ID      string    `json:"id"`
	Text    string    `json:"text"`
	Created time.Time `json:"created"`
	Read    bool      `json:"read"`
}

// keep bounds the feed; older items fall off the front.
const keep = 300

// Store persists one identity's deliveries.
type Store struct{ path string }

// NewStore stores deliveries under dir/deliveries.json.
func NewStore(dir string) *Store {
	return &Store{path: filepath.Join(dir, "deliveries.json")}
}

func (s *Store) load() []Item {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var out []Item
	_ = json.Unmarshal(raw, &out)
	return out
}

func (s *Store) save(list []Item) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0o600)
}

// Add appends a delivery and returns it.
func (s *Store) Add(text string) (Item, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return Item{}, err
	}
	it := Item{ID: hex.EncodeToString(b), Text: text, Created: time.Now()}
	list := append(s.load(), it)
	if len(list) > keep {
		list = list[len(list)-keep:]
	}
	return it, s.save(list)
}

// List returns all items, oldest first (clients usually reverse).
func (s *Store) List() []Item { return s.load() }

// Unread counts items not yet marked read.
func (s *Store) Unread() int {
	n := 0
	for _, it := range s.load() {
		if !it.Read {
			n++
		}
	}
	return n
}

// MarkRead marks the given ids read; with no ids, everything.
func (s *Store) MarkRead(ids ...string) error {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	list := s.load()
	for i := range list {
		if len(want) == 0 || want[list[i].ID] {
			list[i].Read = true
		}
	}
	return s.save(list)
}
