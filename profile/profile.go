// Package profile defines named agent presets — persona + routing tiers +
// whether to delegate grunt work to a fast worker. A profile turns the generic
// engine into a specific assistant (meeting-prep, company-brief, …) without
// touching the core.
//
// Profiles come from two places: a few built-ins compiled in, and *.md files
// dropped into a profiles directory (so adding a new "case" is just writing a
// file, no recompile). A file profile is a markdown body (the orchestrator
// persona) with optional YAML-ish frontmatter for the scalar config:
//
//	---
//	name: company-brief
//	description: Research a company and produce a one-page brief.
//	base_tier: reasoning
//	delegate: true
//	worker_tier: fast
//	---
//	You are a business analyst. Given a company name, produce a brief...
package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flakerimi/harness/router"
)

// Profile is a declarative agent preset.
type Profile struct {
	Name        string
	Description string
	Persona     string      // orchestrator system prompt
	BaseTier    router.Tier // tier for the main (orchestrator) loop

	// Delegation: when true, the orchestrator gets a `delegate` tool that runs
	// subtasks on a fast worker, so research is cheap and only synthesis runs at
	// the expensive tier.
	Delegate      bool
	WorkerPersona string
	WorkerTier    router.Tier

	Source string // "built-in" or the file path it was loaded from
}

// DefaultWorkerPersona is the research persona used by delegated workers when a
// profile doesn't supply its own.
const DefaultWorkerPersona = `You are a research assistant. Given one focused subtask, use the available web search and fetch tools to find concrete, current, verifiable facts. Return a tight, well-organized summary (5-10 lines). Include the source URL for any non-obvious claim. If you cannot find something, say so explicitly — never invent details, names, numbers, or URLs.`

var builtins = map[string]Profile{
	personalProfile.Name: personalProfile,
	workProfile.Name:     workProfile,
}

// Dirs returns the directories scanned for *.md profile files: a project-local
// ./profiles, plus $HARNESS_PROFILES_DIR (or <user-config-dir>/harness/profiles).
func Dirs() []string {
	dirs := []string{"profiles"}
	if d := os.Getenv("HARNESS_PROFILES_DIR"); d != "" {
		dirs = append(dirs, d)
	} else if ucd, err := os.UserConfigDir(); err == nil {
		dirs = append(dirs, filepath.Join(ucd, "harness", "profiles"))
	}
	return dirs
}

func loadAll() (map[string]Profile, []error) {
	m := map[string]Profile{}
	for k, v := range builtins {
		v.Source = "built-in"
		m[k] = v
	}
	var errs []error
	for _, dir := range Dirs() {
		files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
		for _, f := range files {
			p, err := loadFromFile(f)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			m[p.Name] = p // file profiles override built-ins of the same name
		}
	}
	return m, errs
}

// Get resolves a profile by name (built-in or file).
func Get(name string) (Profile, bool) {
	all, _ := loadAll()
	p, ok := all[name]
	return p, ok
}

// Names lists available profile names, sorted.
func Names() []string {
	all, _ := loadAll()
	out := make([]string, 0, len(all))
	for k := range all {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// List returns all profiles (sorted) plus any file-load errors — for the
// `profiles` introspection command.
func List() ([]Profile, []error) {
	all, errs := loadAll()
	out := make([]Profile, 0, len(all))
	for _, p := range all {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, errs
}

func loadFromFile(path string) (Profile, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Profile{}, err
	}
	fm, persona := splitFrontmatter(string(body))

	p := Profile{
		Name:        fm["name"],
		Description: fm["description"],
		Persona:     strings.TrimSpace(persona),
		BaseTier:    router.ParseTier(fm["base_tier"], router.TierReasoning),
		Delegate:    strings.EqualFold(fm["delegate"], "true"),
		WorkerTier:  router.ParseTier(fm["worker_tier"], router.TierFast),
		Source:      path,
	}
	if p.Name == "" {
		p.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	if p.Delegate {
		p.WorkerPersona = DefaultWorkerPersona
	}
	if p.Persona == "" {
		return Profile{}, fmt.Errorf("profile %s: empty persona", path)
	}
	return p, nil
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
		body = body[i+1:] // drop the rest of the closing fence line
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
