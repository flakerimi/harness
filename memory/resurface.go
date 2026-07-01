package memory

import (
	"os"
	"path/filepath"
	"time"
)

// Resurface picks one memory worth proactively revisiting: the least-recently
// updated note that is also older than minAge, so freshly captured items aren't
// echoed straight back at the user. It returns false when nothing qualifies —
// an empty store, or everything still too new. Pair it with Touch to rotate a
// surfaced note to the back of the queue so a series of check-ins works through
// the whole store instead of repeating one item.
func (s *Store) Resurface(minAge time.Duration, now time.Time) (Memory, bool) {
	mems, err := s.Load()
	if err != nil {
		return Memory{}, false
	}
	var pick Memory
	found := false
	for _, m := range mems {
		if now.Sub(m.Updated) < minAge {
			continue // too fresh to resurface
		}
		if !found || m.Updated.Before(pick.Updated) {
			pick, found = m, true
		}
	}
	return pick, found
}

// Touch bumps a memory's modification time to now, so a just-surfaced note sorts
// to the back of the resurfacing queue — an mtime-based rotation that needs no
// extra state file. A missing memory returns the underlying os error.
func (s *Store) Touch(name string, now time.Time) error {
	path := filepath.Join(s.dir, slugify(name)+".md")
	return os.Chtimes(path, now, now)
}
