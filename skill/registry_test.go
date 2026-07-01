package skill

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTreeSearch(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "haiku", "Write a haiku poem.", "5-7-5.")
	writeSkill(t, root, "meeting-prep", "Prepare for a meeting.", "Research attendees.")

	src := tree{root}
	ctx := context.Background()

	all, _ := src.Search(ctx, "")
	if len(all) != 2 {
		t.Fatalf("empty query should return all skills, got %d", len(all))
	}

	// Match on description as well as name.
	hits, _ := src.Search(ctx, "meeting")
	if len(hits) != 1 || hits[0].Name != "meeting-prep" {
		t.Fatalf("query 'meeting' = %+v", hits)
	}
	hits, _ = src.Search(ctx, "poem")
	if len(hits) != 1 || hits[0].Name != "haiku" {
		t.Fatalf("query 'poem' should match by description, got %+v", hits)
	}
	if none, _ := src.Search(ctx, "nonexistent"); len(none) != 0 {
		t.Fatalf("no match expected, got %+v", none)
	}
}

func TestTreeInstall(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "haiku", "Write a haiku poem.", "5-7-5.")
	// A bundled resource alongside SKILL.md must be copied too.
	if err := os.WriteFile(filepath.Join(root, "haiku", "example.txt"), []byte("an old pond"), 0o600); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	s, err := tree{root}.Install(context.Background(), "haiku", dst)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "haiku" || s.Body != "5-7-5." {
		t.Fatalf("installed skill = %+v", s)
	}
	if _, err := os.Stat(filepath.Join(dst, "haiku", "SKILL.md")); err != nil {
		t.Errorf("SKILL.md not installed: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "haiku", "example.txt")); err != nil || string(b) != "an old pond" {
		t.Errorf("bundled resource not copied: %q err=%v", b, err)
	}

	// An installed skill is discoverable via the normal loader.
	skills, _ := Load(dst)
	var found bool
	for _, sk := range skills {
		if sk.Name == "haiku" {
			found = true
		}
	}
	if !found {
		t.Error("installed skill not discoverable via Load")
	}
}

func TestTreeInstallErrors(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "haiku", "Write a haiku poem.", "5-7-5.")
	src := tree{root}

	if _, err := src.Install(context.Background(), "missing", t.TempDir()); err == nil {
		t.Error("installing an unknown skill should error")
	}
	// Path-traversal names must be rejected before touching the filesystem.
	for _, bad := range []string{"../escape", "a/b", "..", ""} {
		if _, err := src.Install(context.Background(), bad, t.TempDir()); err == nil {
			t.Errorf("name %q should be rejected", bad)
		}
	}
}

func TestGitSource(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Build a local origin repo of skill folders — no network involved.
	origin := t.TempDir()
	writeSkill(t, origin, "haiku", "Write a haiku poem.", "5-7-5.")
	writeSkill(t, origin, "standup", "Run a daily standup.", "Yesterday, today, blockers.")
	gitInit(t, origin)

	gs := &GitSource{URL: origin, Cache: filepath.Join(t.TempDir(), "clone")}
	ctx := context.Background()

	// First Search clones the repo into the cache.
	entries, err := gs.Search(ctx, "standup")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "standup" {
		t.Fatalf("search = %+v", entries)
	}
	if _, err := os.Stat(filepath.Join(gs.Cache, ".git")); err != nil {
		t.Errorf("expected a clone in the cache: %v", err)
	}

	// Install pulls the folder out of the cached clone.
	dst := t.TempDir()
	s, err := gs.Install(ctx, "haiku", dst)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if s.Name != "haiku" {
		t.Fatalf("installed = %+v", s)
	}
	if _, err := os.Stat(filepath.Join(dst, "haiku", "SKILL.md")); err != nil {
		t.Errorf("skill not installed: %v", err)
	}
}

func TestGitSourceNoURL(t *testing.T) {
	gs := &GitSource{Cache: t.TempDir()}
	if _, err := gs.Search(context.Background(), ""); err == nil {
		t.Error("a GitSource with no URL should error")
	}
}

// gitInit turns dir into a committed git repo, with identity set locally so the
// commit works even where no global git identity is configured.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "test@harness.local"},
		{"config", "user.name", "harness test"},
		{"add", "."},
		{"-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "skills"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}
