package skill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Entry is a skill advertised by a registry: its name and description, without
// the full body. Search returns these; the body arrives on Install.
type Entry struct {
	Name        string
	Description string
}

// Source is an installable catalog of skills — e.g. a git repo of skill
// folders. Search lists matching entries; Install copies one skill's folder
// into a local skills dir, after which the normal Load machinery discovers it.
// Keeping Source separate from Load means "where a skill comes from" and "how a
// skill is used" stay decoupled: any Source that lands folders on disk works.
type Source interface {
	Search(ctx context.Context, query string) ([]Entry, error)
	Install(ctx context.Context, name, dstDir string) (Skill, error)
}

// tree is a Source backed by a local directory of <name>/SKILL.md folders (a
// checked-out registry). It holds the git-independent scan/copy logic so it can
// be unit-tested without git; GitSource layers cloning on top.
type tree struct{ root string }

// list loads every skill folder directly under the tree root (reusing loadFile,
// so registry skills are validated the same way local ones are).
func (t tree) list() []Skill {
	var skills []Skill
	matches, _ := filepath.Glob(filepath.Join(t.root, "*", "SKILL.md"))
	for _, path := range matches {
		s, err := loadFile(path)
		if err != nil {
			continue // skip malformed entries rather than failing the whole catalog
		}
		skills = append(skills, s)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills
}

func (t tree) Search(_ context.Context, query string) ([]Entry, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []Entry
	for _, s := range t.list() {
		if q == "" ||
			strings.Contains(strings.ToLower(s.Name), q) ||
			strings.Contains(strings.ToLower(s.Description), q) {
			out = append(out, Entry{Name: s.Name, Description: s.Description})
		}
	}
	return out, nil
}

func (t tree) Install(_ context.Context, name, dstDir string) (Skill, error) {
	if !validSkillName(name) {
		return Skill{}, fmt.Errorf("invalid skill name %q", name)
	}
	src := filepath.Join(t.root, name)
	if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
		return Skill{}, fmt.Errorf("skill %q not found in registry", name)
	}
	dst := filepath.Join(dstDir, name)
	if err := copyTree(src, dst); err != nil {
		return Skill{}, err
	}
	return loadFile(filepath.Join(dst, "SKILL.md"))
}

// validSkillName rejects anything that isn't a single plain folder name, so a
// registry entry can't write outside the destination dir via "../" or an
// absolute path.
func validSkillName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, `/\`) && !strings.Contains(name, "..")
}

// GitSource is a Source backed by a git repository of skill folders. It keeps a
// shallow clone in a cache dir and serves skills from the checked-out tree, so
// the registry is versioned and browsable offline between refreshes.
type GitSource struct {
	URL     string
	Cache   string // local clone directory
	Refresh bool   // pull the latest before serving (else use the cached clone)
}

// NewGitSource builds a git-backed registry for url, caching the clone under
// <user-config>/harness/registry/<hash(url)> so distinct registries don't
// collide.
func NewGitSource(url string) *GitSource {
	return &GitSource{URL: url, Cache: registryCacheDir(url)}
}

func registryCacheDir(url string) string {
	sum := sha256.Sum256([]byte(url))
	name := hex.EncodeToString(sum[:])[:12]
	base := "."
	if ucd, err := os.UserConfigDir(); err == nil {
		base = filepath.Join(ucd, "harness")
	}
	return filepath.Join(base, "registry", name)
}

func (g *GitSource) Search(ctx context.Context, query string) ([]Entry, error) {
	if err := g.sync(ctx); err != nil {
		return nil, err
	}
	return tree{g.Cache}.Search(ctx, query)
}

func (g *GitSource) Install(ctx context.Context, name, dstDir string) (Skill, error) {
	if err := g.sync(ctx); err != nil {
		return Skill{}, err
	}
	return tree{g.Cache}.Install(ctx, name, dstDir)
}

// sync ensures the cache holds a usable clone: it clones on first use, and pulls
// only when Refresh is set (so a plain search/add uses the cached copy and stays
// fast/offline).
func (g *GitSource) sync(ctx context.Context) error {
	if strings.TrimSpace(g.URL) == "" {
		return fmt.Errorf("no skills registry configured (set skills.registry, or HARNESS_SKILLS_REGISTRY)")
	}
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git not found on PATH — needed for the skills registry")
	}
	if _, err := os.Stat(filepath.Join(g.Cache, ".git")); err == nil {
		if !g.Refresh {
			return nil
		}
		return git(ctx, g.Cache, "pull", "--quiet", "--ff-only")
	}
	if err := os.MkdirAll(filepath.Dir(g.Cache), 0o755); err != nil {
		return err
	}
	return git(ctx, "", "clone", "--quiet", "--depth", "1", g.URL, g.Cache)
}

// git runs a git subcommand (in dir, or the current dir when empty), surfacing
// stderr in the error so failures are diagnosable.
func git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// copyTree recursively copies a skill folder src → dst (SKILL.md plus any
// bundled scripts/resources), creating dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
