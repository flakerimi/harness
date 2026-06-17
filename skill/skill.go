// Package skill loads Agent Skills — the open SKILL.md format (from
// agentskills.io / Anthropic), supported across Claude Code, Cursor, OpenCode,
// and others. A skill is a folder with a SKILL.md (frontmatter name +
// description, then instructions; optional bundled scripts/resources).
//
// Skills reach the agent via progressive disclosure: each skill's name and
// description go into the system prompt (cheap), and the model calls the
// load_skill tool to pull a skill's full instructions only when a task matches.
package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flakerimi/harness/tool"
)

// Skill is one loaded SKILL.md.
type Skill struct {
	Name        string
	Description string
	Body        string // full instructions (the SKILL.md body)
	Dir         string // skill folder (holds optional scripts/references)
}

// Dirs returns the directories scanned for <skill>/SKILL.md: a project-local
// ./skills, plus $HARNESS_SKILLS_DIR (or <user-config-dir>/harness/skills).
func Dirs() []string {
	dirs := []string{"skills"}
	if d := os.Getenv("HARNESS_SKILLS_DIR"); d != "" {
		dirs = append(dirs, d)
	} else if ucd, err := os.UserConfigDir(); err == nil {
		dirs = append(dirs, filepath.Join(ucd, "harness", "skills"))
	}
	return dirs
}

// Load scans the skill dirs for <skill>/SKILL.md files. The first skill found
// for a given name wins; later dirs don't override earlier ones.
func Load() ([]Skill, []error) {
	seen := map[string]bool{}
	var skills []Skill
	var errs []error
	for _, dir := range Dirs() {
		matches, _ := filepath.Glob(filepath.Join(dir, "*", "SKILL.md"))
		for _, path := range matches {
			s, err := loadFile(path)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			skills = append(skills, s)
		}
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, errs
}

func loadFile(path string) (Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	fm, instructions := splitFrontmatter(string(raw))
	s := Skill{
		Name:        fm["name"],
		Description: fm["description"],
		Body:        strings.TrimSpace(instructions),
		Dir:         filepath.Dir(path),
	}
	if s.Name == "" {
		s.Name = filepath.Base(filepath.Dir(path))
	}
	if s.Description == "" {
		return Skill{}, fmt.Errorf("skill %s: missing description", path)
	}
	if s.Body == "" {
		return Skill{}, fmt.Errorf("skill %s: empty instructions", path)
	}
	return s, nil
}

// DiscoveryText renders the system-prompt section that advertises the skills.
func DiscoveryText(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Skills\nYou have specialized skills available. Before performing a task that matches one of them, call the load_skill tool with its name to load the full instructions, then follow them exactly.\n")
	for _, s := range skills {
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
	}
	return strings.TrimRight(b.String(), "\n")
}

// LoadTool is the load_skill tool: returns a skill's full instructions.
type LoadTool struct {
	skills map[string]Skill
	names  []string
}

// NewLoadTool builds the load_skill tool over a set of skills.
func NewLoadTool(skills []Skill) *LoadTool {
	m := make(map[string]Skill, len(skills))
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		m[s.Name] = s
		names = append(names, s.Name)
	}
	return &LoadTool{skills: m, names: names}
}

func (t *LoadTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "load_skill",
		Description: "Load the full instructions for a named skill before performing a task that matches it.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "The skill name to load."},
			},
			"required": []string{"name"},
		},
	}
}

func (t *LoadTool) Run(_ context.Context, input json.RawMessage, _ *tool.Env) (tool.Result, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	s, ok := t.skills[args.Name]
	if !ok {
		return tool.Result{
			Content: fmt.Sprintf("unknown skill %q (available: %s)", args.Name, strings.Join(t.names, ", ")),
			IsError: true,
		}, nil
	}
	return tool.Result{Content: s.Body}, nil
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
