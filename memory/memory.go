// Package memory gives the assistant persistent, per-profile memory — durable
// facts and preferences it carries across invocations. Each identity has its
// own memory directory (<profile>/memory/*.md), one fact per file, mirroring
// Construct's and a coding agent's file-based memory.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Memory is one stored fact.
type Memory struct {
	Name    string
	Content string
	Path    string
	Updated time.Time // file mtime — when it was last written or surfaced
}

// Store reads and writes a profile's memory directory.
type Store struct {
	dir string
}

// NewStore targets a memory directory (e.g. <profileDir>/memory).
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir returns the memory directory.
func (s *Store) Dir() string { return s.dir }

// Load returns all memories, sorted by name. A missing dir yields none.
func (s *Store) Load() ([]Memory, error) {
	matches, err := filepath.Glob(filepath.Join(s.dir, "*.md"))
	if err != nil {
		return nil, err
	}
	out := make([]Memory, 0, len(matches))
	for _, p := range matches {
		body, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		content := strings.TrimSpace(string(body))
		if content == "" {
			continue
		}
		m := Memory{
			Name:    strings.TrimSuffix(filepath.Base(p), ".md"),
			Content: content,
			Path:    p,
		}
		if fi, err := os.Stat(p); err == nil {
			m.Updated = fi.ModTime()
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Save writes a memory. If name is empty, a slug is derived from the content.
// Returns the file path.
func (s *Store) Save(name, content string) (string, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("memory: empty content")
	}
	if name == "" {
		name = slugify(content)
	} else {
		name = slugify(name)
	}
	if name == "" {
		name = "note"
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(s.dir, name+".md")
	if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Context renders every memory for injection into the system prompt, plus the
// remember nudge. Kept for callers that want the full set; Digest bounds it.
func Context(mems []Memory) string { return Digest(mems, 0) }

// Digest renders up to max memories (max <= 0 = all) for injection into the
// system prompt, plus the remember nudge. When the store holds more than max,
// it injects the first max and notes that the rest are searchable via the
// recall tool — so a large second-brain memory doesn't bloat every prompt while
// staying fully reachable on demand. Empty memories yield just the Instruction.
func Digest(mems []Memory, max int) string {
	if len(mems) == 0 {
		return Instruction
	}
	shown, extra := mems, 0
	if max > 0 && len(mems) > max {
		shown, extra = mems[:max], len(mems)-max
	}
	var b strings.Builder
	b.WriteString("## What you remember about the user\n")
	for _, m := range shown {
		fmt.Fprintf(&b, "- %s\n", oneLine(m.Content))
	}
	if extra > 0 {
		fmt.Fprintf(&b, "- …and %d more — use the recall tool to search them.\n", extra)
	}
	b.WriteString("\n")
	b.WriteString(Instruction)
	return b.String()
}

// Instruction tells the assistant when to persist a memory.
const Instruction = "When the user shares something durable — a preference, a fact about them, an ongoing project, a person or company they deal with — call the remember tool to save it for future conversations. Don't save one-off task details or anything they ask you not to keep."

var reSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = reSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	return s
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
