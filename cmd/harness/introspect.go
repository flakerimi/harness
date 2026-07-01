package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flakerimi/harness/app"
	"github.com/flakerimi/harness/config"
	"github.com/flakerimi/harness/memory"
	"github.com/flakerimi/harness/profile"
	"github.com/flakerimi/harness/skill"
	"github.com/flakerimi/harness/subagent"
)

// runAgents lists the available specialist sub-agents (agents/*.md) the
// orchestrator can dispatch to, including the active identity's own.
func runAgents(args []string) {
	fs := flag.NewFlagSet("agents", flag.ExitOnError)
	profileFlag := fs.String("profile", "", "identity profile (default from config)")
	_ = fs.Parse(args)
	name := *profileFlag
	if name == "" {
		name = activeProfile()
	}
	var dirs []string
	if name != "" {
		dirs = append(dirs, profile.AgentsDir(name))
	}
	specs, errs := subagent.Load(dirs...)
	for _, s := range specs {
		tools := "all tools"
		if len(s.Tools) > 0 {
			tools = strings.Join(s.Tools, ", ")
		}
		tier := s.Tier
		if tier == "" {
			tier = "fast"
		}
		fmt.Printf("%-16s %s\n", s.Name, s.Description)
		fmt.Printf("    tier=%s tools=[%s]  [%s]\n", tier, tools, s.Dir)
	}
	if len(specs) == 0 {
		fmt.Println("(no specialists — add one with agents/<name>.md)")
	}
	allDirs := append(dirs, subagent.Dirs()...)
	fmt.Fprintf(os.Stderr, "\nagent dirs: %s\n", strings.Join(allDirs, ", "))
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "warning:", e)
	}
}

// runMemory lists the active identity's durable memories.
func runMemory(args []string) {
	// Optional subcommand: harness memory search [-profile p] <query>
	searchMode := false
	if len(args) > 0 && args[0] == "search" {
		searchMode, args = true, args[1:]
	}
	fs := flag.NewFlagSet("memory", flag.ExitOnError)
	profileFlag := fs.String("profile", "", "identity profile (default from config)")
	_ = fs.Parse(args)
	name := *profileFlag
	if name == "" {
		name = activeProfile()
	}
	store := memory.NewStore(profile.MemoryDir(name))

	if searchMode {
		query := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if query == "" {
			fmt.Fprintln(os.Stderr, "usage: harness memory search [-profile <name>] <query>")
			os.Exit(2)
		}
		hits, err := store.Search(query, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if len(hits) == 0 {
			fmt.Println("(no matching memories)")
			return
		}
		for _, m := range hits {
			fmt.Printf("- %s: %s\n", m.Name, m.Content)
		}
		return
	}

	mems, err := store.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	label := name
	if label == "" {
		label = "(default)"
	}
	fmt.Printf("memory for %q  [%s]\n", label, store.Dir())
	for _, m := range mems {
		fmt.Printf("- %s: %s\n", m.Name, m.Content)
	}
	if len(mems) == 0 {
		fmt.Println("(nothing remembered yet)")
	}
}

// runSkills lists the loaded Agent Skills (SKILL.md folders) — the procedural
// know-how available to agents via progressive disclosure. The active identity's
// own learned-skills dir is included. Subcommands reach the git-backed registry:
//
//	harness skills                 # list installed skills (default)
//	harness skills search <query>  # find skills in the registry
//	harness skills add <name>      # install one into the shared skills dir
func runSkills(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "search":
			runSkillsSearch(args[1:])
			return
		case "add", "install":
			runSkillsAdd(args[1:])
			return
		}
	}
	fs := flag.NewFlagSet("skills", flag.ExitOnError)
	profileFlag := fs.String("profile", "", "identity profile (default from config)")
	_ = fs.Parse(args)
	name := *profileFlag
	if name == "" {
		name = activeProfile()
	}
	var dirs []string
	if name != "" {
		dirs = append(dirs, profile.SkillsDir(name))
	}
	skills, errs := skill.Load(dirs...)
	for _, s := range skills {
		fmt.Printf("%-16s %s\n", s.Name, s.Description)
		fmt.Printf("    [%s]\n", s.Dir)
	}
	if len(skills) == 0 {
		fmt.Println("(no skills)")
	}
	allDirs := append(dirs, skill.Dirs()...)
	fmt.Fprintf(os.Stderr, "\nskill dirs: %s\n", strings.Join(allDirs, ", "))
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "warning:", e)
	}
}

// runSkillsSearch queries the configured registry and prints matching skills.
func runSkillsSearch(args []string) {
	fs := flag.NewFlagSet("skills search", flag.ExitOnError)
	refresh := fs.Bool("refresh", false, "pull the latest registry before searching")
	_ = fs.Parse(args)

	src, err := skillsRegistry(*refresh)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	entries, err := src.Search(context.Background(), strings.Join(fs.Args(), " "))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if len(entries) == 0 {
		fmt.Println("(no matching skills)")
		return
	}
	for _, e := range entries {
		fmt.Printf("%-16s %s\n", e.Name, e.Description)
	}
	fmt.Fprintf(os.Stderr, "\n%d skill(s) — install with: harness skills add <name>\n", len(entries))
}

// runSkillsAdd installs a registry skill into the shared skills dir (or a
// profile's own with -profile), where the loader then discovers it.
func runSkillsAdd(args []string) {
	fs := flag.NewFlagSet("skills add", flag.ExitOnError)
	profileFlag := fs.String("profile", "", "install into this identity's skills dir instead of the shared one")
	refresh := fs.Bool("refresh", false, "pull the latest registry before installing")
	_ = fs.Parse(args)

	name := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if name == "" {
		fmt.Fprintln(os.Stderr, "usage: harness skills add [-profile <name>] <skill>")
		os.Exit(2)
	}
	src, err := skillsRegistry(*refresh)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	dst := skill.UserSkillsDir()
	if *profileFlag != "" {
		dst = profile.SkillsDir(*profileFlag)
	}
	s, err := src.Install(context.Background(), name, dst)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("installed %s → %s\n", s.Name, filepath.Join(dst, s.Name))
	fmt.Printf("    %s\n", s.Description)
}

// skillsRegistry builds the git-backed skills registry from config, erroring
// clearly when none is set.
func skillsRegistry(refresh bool) (skill.Source, error) {
	cfg, _ := config.Load()
	url := cfg.SkillsRegistry()
	if url == "" {
		return nil, fmt.Errorf("no skills registry configured — set skills.registry (a git repo URL) in %s, or $HARNESS_SKILLS_REGISTRY", config.Path())
	}
	gs := skill.NewGitSource(url)
	gs.Refresh = refresh
	return gs, nil
}

// runProfiles lists the available agent profiles (built-in + file-based) — the
// set of "cases" the harness can run. Add a case by dropping a .md file in a
// profiles directory.
func runProfiles(_ []string) {
	profs, errs := profile.List()
	for _, p := range profs {
		fmt.Printf("%-16s %s\n", p.Name, p.Description)
		fmt.Printf("    tier=%s delegate=%v  [%s]\n", p.BaseTier, p.Delegate, p.Source)
	}
	if len(profs) == 0 {
		fmt.Println("(no profiles)")
	}
	fmt.Fprintf(os.Stderr, "\nprofile dirs: %s\n", strings.Join(profile.Dirs(), ", "))
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "warning:", e)
	}
}

// runConfig prints the config file location and the resolved search settings
// (secrets masked) — so "where is my config / am I set up" is one command.
func runConfig(_ []string) {
	fmt.Printf("config file: %s\n", config.Path())
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	u := firstNonEmpty(cfg.Search.SearxngURL, os.Getenv("HARNESS_SEARXNG_URL"))
	tok := firstNonEmpty(cfg.Search.SearxngToken, os.Getenv("HARNESS_SEARXNG_TOKEN"))
	fmt.Printf("search.searxng_url:   %s\n", maskURLSecret(u))
	fmt.Printf("search.searxng_token: %s\n", maskSecret(tok))
	if u == "" {
		fmt.Println("\n(no SearXNG configured — web_search falls back to DuckDuckGo)")
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func maskSecret(s string) string {
	if s == "" {
		return "(unset)"
	}
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "…" + s[len(s)-4:]
}

// maskURLSecret hides the password in a userinfo-bearing URL.
func maskURLSecret(raw string) string {
	if raw == "" {
		return "(unset)"
	}
	at := strings.LastIndex(raw, "@")
	scheme := strings.Index(raw, "://")
	if at < 0 || scheme < 0 || at < scheme {
		return raw
	}
	userinfo := raw[scheme+3 : at]
	user, _, hasPass := strings.Cut(userinfo, ":")
	if !hasPass {
		return raw
	}
	return raw[:scheme+3] + user + ":****@" + raw[at+1:]
}

// runConnectors prints what the harness is connected to and the tools each
// connector exposes — the introspection surface for "where are we connected".
func runConnectors(args []string) {
	fs := flag.NewFlagSet("connectors", flag.ExitOnError)
	_ = fs.Parse(args)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, c := range app.Connectors(false, activeProfile()).Connectors() {
		if cl, ok := c.(interface{ Close() error }); ok {
			defer cl.Close()
		}
		st := c.Status(ctx)
		mark := "✗ not connected"
		if st.Connected {
			mark = "✓ connected"
		}
		fmt.Printf("%s  %s — %s\n", mark, c.Name(), st.Detail)
		ts, err := c.Tools(ctx)
		if err != nil {
			fmt.Printf("    (error listing tools: %v)\n", err)
			continue
		}
		for _, t := range ts {
			s := t.Spec()
			fmt.Printf("    • %s — %s\n", s.Name, s.Description)
		}
	}
}
