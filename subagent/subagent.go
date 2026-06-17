// Package subagent loads declarative specialist definitions — agents/<name>.md
// files the orchestrator can dispatch focused tasks to. A sub-agent is a
// separate agent run with its own persona, model tier, and tool subset (unlike
// a skill, which is a procedure the current agent follows itself). Definitions
// are file-dropped and per-identity scopable, exactly like skills and profiles.
//
//	---
//	name: researcher
//	description: Deep web research on a person or company; returns a sourced brief.
//	tier: fast
//	tools: web_search, web_fetch
//	---
//	You are a research specialist. Given one subject, find concrete, current,
//	sourced facts and return a tight brief. Never invent details.
package subagent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Spec is one declarative sub-agent definition.
type Spec struct {
	Name        string
	Description string
	Tier        string   // routing tier: fast | balanced | reasoning (default fast)
	Tools       []string // allowed tool names; empty = every tool the orchestrator has
	Persona     string   // the specialist's system prompt (the file body)
	Dir         string   // the dir it was loaded from
}

// Dirs returns the directories scanned for agents/<name>.md: a project-local
// ./agents, plus $HARNESS_AGENTS_DIR (or <user-config-dir>/harness/agents).
func Dirs() []string {
	dirs := []string{"agents"}
	if d := os.Getenv("HARNESS_AGENTS_DIR"); d != "" {
		dirs = append(dirs, d)
	} else if ucd, err := os.UserConfigDir(); err == nil {
		dirs = append(dirs, filepath.Join(ucd, "harness", "agents"))
	}
	return dirs
}

// Load scans the agent dirs (extraDirs first — e.g. a profile's own agents —
// then the shared dirs) for *.md specialist files. The first spec found for a
// given name wins, so a profile's specialists take precedence.
func Load(extraDirs ...string) ([]Spec, []error) {
	seen := map[string]bool{}
	var specs []Spec
	var errs []error
	dirs := append(append([]string{}, extraDirs...), Dirs()...)
	for _, dir := range dirs {
		files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
		for _, path := range files {
			s, err := loadFile(path)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			specs = append(specs, s)
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, errs
}

func loadFile(path string) (Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, err
	}
	fm, body := splitFrontmatter(string(raw))
	s := Spec{
		Name:        fm["name"],
		Description: fm["description"],
		Tier:        fm["tier"],
		Persona:     strings.TrimSpace(body),
		Dir:         filepath.Dir(path),
	}
	if s.Name == "" {
		s.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	for tn := range strings.SplitSeq(fm["tools"], ",") {
		if tn = strings.TrimSpace(tn); tn != "" {
			s.Tools = append(s.Tools, tn)
		}
	}
	if s.Description == "" {
		return Spec{}, fmt.Errorf("subagent %s: missing description", path)
	}
	if s.Persona == "" {
		return Spec{}, fmt.Errorf("subagent %s: empty persona", path)
	}
	return s, nil
}

// DiscoveryText renders the system-prompt section advertising the specialists,
// so the orchestrator knows whom it can dispatch to.
func DiscoveryText(specs []Spec) string {
	if len(specs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Specialists\nYou can hand a focused, self-contained task to a specialist sub-agent with the dispatch tool. It runs separately (no access to this conversation) and returns its result. Available specialists:\n")
	for _, s := range specs {
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
	}
	return strings.TrimRight(b.String(), "\n")
}

// splitFrontmatter parses optional leading "---\n … \n---" frontmatter (simple
// key: value lines) and returns the map plus the remaining body.
func splitFrontmatter(text string) (map[string]string, string) {
	fm := map[string]string{}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return fm, text
	}
	header, body, ok := strings.Cut(text[len("---\n"):], "\n---")
	if !ok {
		return fm, text
	}
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[i+1:]
	}
	for line := range strings.SplitSeq(header, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fm[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return fm, body
}
