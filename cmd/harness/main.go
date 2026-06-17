// Command harness is a thin reference CLI built on the framework. Subcommands:
//
//	harness login [-provider claude]   # OAuth login → writes auth.json
//	harness [flags] <prompt>           # run one agent turn (default)
//
// It exists to dogfood the library — the real product is the packages it imports.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/auth"
	"github.com/flakerimi/harness/config"
	"github.com/flakerimi/harness/connector"
	"github.com/flakerimi/harness/connector/google"
	"github.com/flakerimi/harness/connector/mcp"
	"github.com/flakerimi/harness/memory"
	"github.com/flakerimi/harness/profile"
	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/router"
	"github.com/flakerimi/harness/skill"
	"github.com/flakerimi/harness/tool"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "login":
			runLogin(os.Args[2:])
			return
		case "connect":
			runConnect(os.Args[2:])
			return
		case "connectors":
			runConnectors(os.Args[2:])
			return
		case "config":
			runConfig(os.Args[2:])
			return
		case "profiles":
			runProfiles(os.Args[2:])
			return
		case "skills":
			runSkills(os.Args[2:])
			return
		case "memory":
			runMemory(os.Args[2:])
			return
		}
	}
	runAgent(os.Args[1:])
}

// runMemory lists the active identity's durable memories.
func runMemory(args []string) {
	fs := flag.NewFlagSet("memory", flag.ExitOnError)
	profileFlag := fs.String("profile", "", "identity profile (default from config)")
	_ = fs.Parse(args)
	name := *profileFlag
	if name == "" {
		name = activeProfile()
	}
	store := memory.NewStore(profile.MemoryDir(name))
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
// own learned-skills dir is included.
func runSkills(args []string) {
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

// defaultConnectors wires the integrations available to this harness instance.
// Native built-ins are always present; external connectors (calendar, mail,
// search via MCP) are added here as they're built — never hardcoded deeper in.
func defaultConnectors(allowShell bool, profileName string) *connector.Registry {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: config:", err)
	}
	tools := []tool.Tool{
		tool.ReadFile{},
		tool.WebFetch{},
		tool.WebSearch{SearxngURL: cfg.Search.SearxngURL, SearxngToken: cfg.Search.SearxngToken},
	}
	if allowShell {
		tools = append(tools, tool.Bash{})
	}
	r := connector.NewRegistry()
	r.Add(connector.NewNative("builtin", tools...))

	// Google connector when an OAuth client is configured — scoped to this
	// profile's own auth file, so different identities connect different Google
	// accounts (connect via `harness connect google`).
	if id, secret := cfg.GoogleClient(); id != "" && secret != "" {
		r.Add(google.New(auth.NewStore(profile.AuthFile(profileName)), id, secret))
	}

	// External connectors come from mcp.json — add a server there and it shows
	// up here with no code change. Nothing vendor-specific is baked in.
	cfgs, err := mcp.LoadConfig(mcpConfigPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: mcp config:", err)
	}
	for _, c := range cfgs {
		r.Add(mcp.New(c))
	}
	return r
}

func mcpConfigPath() string {
	if v := os.Getenv("HARNESS_MCP_FILE"); v != "" {
		return v
	}
	return "mcp.json"
}

// runConnectors prints what the harness is connected to and the tools each
// connector exposes — the introspection surface for "where are we connected".
func runConnectors(args []string) {
	fs := flag.NewFlagSet("connectors", flag.ExitOnError)
	_ = fs.Parse(args)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, c := range defaultConnectors(false, activeProfile()).Connectors() {
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

func authFileDefault() string {
	if v := os.Getenv("HARNESS_AUTH_FILE"); v != "" {
		return v
	}
	return "auth.json"
}

// activeProfile resolves the identity profile from env/config (no flag context).
func activeProfile() string {
	cfg, _ := config.Load()
	return cfg.Profile()
}

func runLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	prov := fs.String("provider", "claude", "provider to log in (claude)")
	authFile := fs.String("auth-file", authFileDefault(), "credential file to write")
	urlOnly := fs.Bool("url-only", false, "print the authorize URL and exit (no browser, no wait)")
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	store := auth.NewStore(*authFile)
	onURL := func(u string) { fmt.Fprintln(os.Stderr, "If your browser didn't open, visit:\n  "+u) }

	switch strings.ToLower(*prov) {
	case "claude", "anthropic":
		if *urlOnly {
			u, _, err := auth.AnthropicAuthURL()
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			fmt.Println(u)
			return
		}
		fmt.Fprintln(os.Stderr, "Opening browser for Claude login…")
		if _, err := auth.AnthropicLogin(ctx, store, "claude", onURL); err != nil {
			fmt.Fprintln(os.Stderr, "login failed:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "✓ logged in — credentials saved to %s\n", *authFile)

	case "google", "gemini":
		fmt.Fprintln(os.Stderr, "Gemini uses an API key — set GEMINI_API_KEY (or GOOGLE_API_KEY) and run with -provider gemini.")
		fmt.Fprintln(os.Stderr, "For Google Calendar/Mail integration, use: harness connect google")

	default:
		fmt.Fprintf(os.Stderr, "login: unknown provider %q (claude | gemini)\n", *prov)
		os.Exit(2)
	}
}

// runConnect handles `harness connect <service>` — the friendly entry point for
// integrations (Google, …), distinct from `login` (the model provider).
func runConnect(args []string) {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	profileFlag := fs.String("profile", "", "identity to attach the account to (default from config)")
	_ = fs.Parse(args)

	profileName := *profileFlag
	if profileName == "" {
		profileName = activeProfile()
	}
	// Accounts are profile-scoped — store in this identity's own data dir.
	if err := os.MkdirAll(profile.DataDir(profileName), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	authFile := profile.AuthFile(profileName)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	store := auth.NewStore(authFile)
	onURL := func(u string) { fmt.Fprintln(os.Stderr, "If your browser didn't open, visit:\n  "+u) }

	label := profileName
	if label == "" {
		label = "(default)"
	}
	switch strings.ToLower(strings.TrimSpace(fs.Arg(0))) {
	case "google":
		fmt.Fprintf(os.Stderr, "Connecting Google for identity %q…\n", label)
		connectGoogle(ctx, store, authFile, onURL)
	case "":
		fmt.Fprintln(os.Stderr, "usage: harness connect <service> [-profile <name>]   (services: google)")
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "connect: unknown service %q (services: google)\n", fs.Arg(0))
		os.Exit(2)
	}
}

func connectGoogle(ctx context.Context, store *auth.Store, authFile string, onURL func(string)) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: config:", err)
	}
	id, secret := cfg.GoogleClient()
	if id == "" || secret == "" {
		fmt.Fprintln(os.Stderr, "Google needs a Desktop OAuth client — set google.client_id / client_secret in config (or GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET). Create one at console.cloud.google.com (OAuth client type: Desktop app).")
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "Opening browser to connect Google…")
	if _, err := auth.GoogleLogin(ctx, store, id, secret, nil, onURL); err != nil {
		fmt.Fprintln(os.Stderr, "google connect failed:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "✓ Google connected — credentials saved to %s\n", authFile)
}

func runAgent(args []string) {
	fs := flag.NewFlagSet("harness", flag.ExitOnError)
	providerSlug := fs.String("provider", "mock", "provider slug: mock | anthropic|claude | openai | deepseek | gemini | ollama | lmstudio")
	model := fs.String("model", "", "explicit model id — overrides automatic routing")
	system := fs.String("system", "You are a helpful assistant.", "system prompt")
	maxTokens := fs.Int("max-tokens", 4096, "max output tokens")
	root := fs.String("root", ".", "workspace root for filesystem tools")
	profileFlag := fs.String("profile", "", "identity profile (e.g. personal, work); default from config/HARNESS_PROFILE")
	tierFlag := fs.String("tier", "reasoning", "base routing tier: fast | balanced | reasoning")
	route := fs.Bool("route", true, "automatic model routing (ignored when -model is set)")
	classify := fs.Bool("classify", true, "classify task difficulty to pick the base tier")
	escalate := fs.Bool("escalate", true, "escalate a tier when a turn produces nothing usable")
	bash := fs.Bool("bash", false, "enable the bash tool (runs shell commands — trusted skills only)")
	_ = fs.Parse(args)

	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		data, _ := io.ReadAll(os.Stdin)
		prompt = strings.TrimSpace(string(data))
	}
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: harness [flags] <prompt>   (or pipe the prompt on stdin)")
		fs.PrintDefaults()
		os.Exit(2)
	}

	// Identity profile: flag, else config default (HARNESS_PROFILE / default_profile).
	profileName := *profileFlag
	if profileName == "" {
		cfg, _ := config.Load()
		profileName = cfg.Profile()
	}

	prov, err := provider.Build(*providerSlug)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	reg, err := defaultConnectors(*bash, profileName).Tools(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	caps := []string{provider.CapTools, provider.CapCaching}
	opts := agent.Options{
		Model:     *model,
		System:    *system,
		MaxTokens: *maxTokens,
		Caps:      caps,
		Env:       &tool.Env{Root: *root},
	}
	toolReg := reg // tools the orchestrator uses

	// Agent Skills: register the load_skill tool into reg so both the
	// orchestrator and any delegated workers get it, and build the discovery
	// text that advertises skills in the system prompt. For an identity profile,
	// its own learned-skills dir is scanned first (and wins on name conflicts).
	var skillDirs []string
	if profileName != "" {
		skillDirs = append(skillDirs, profile.SkillsDir(profileName))
	}
	skills, skErrs := skill.Load(skillDirs...)
	for _, e := range skErrs {
		fmt.Fprintln(os.Stderr, "warning: skill:", e)
	}
	skillDiscovery := ""
	if len(skills) > 0 {
		reg.Register(skill.NewLoadTool(skills))
		skillDiscovery = skill.DiscoveryText(skills)
	}

	// Routing is on when requested, or implied by a profile.
	var rt *router.Table
	if (*model == "" && *route) || profileName != "" {
		t, rerr := router.LoadTable(modelsConfigPath())
		if rerr != nil {
			fmt.Fprintln(os.Stderr, "warning: models config:", rerr)
			t = router.DefaultTable()
		}
		rt = t
		opts.Router = rt
		opts.BaseTier = router.ParseTier(*tierFlag, router.TierReasoning)
		opts.Classify = *classify
		opts.Escalate = *escalate
	}

	if profileName != "" {
		prof, ok := profile.Get(profileName)
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown profile %q (available: %s)\n", profileName, strings.Join(profile.Names(), ", "))
			os.Exit(2)
		}
		opts.System = prof.Persona
		opts.BaseTier = prof.BaseTier
		opts.Classify = false // the profile sets the orchestrator's tier
		if prof.Delegate {
			workerSystem := prof.WorkerPersona
			if skillDiscovery != "" {
				workerSystem += "\n\n" + skillDiscovery
			}
			orch := tool.NewRegistry()
			for _, t := range reg.All() { // reg already includes load_skill
				orch.Register(t)
			}
			orch.Register(agent.Delegate{
				Provider:  prov,
				Tools:     reg, // worker gets the connector tools + load_skill (no delegate → no recursion)
				Router:    rt,
				Tier:      prof.WorkerTier,
				System:    workerSystem,
				MaxTokens: *maxTokens,
				Caps:      caps,
			})
			toolReg = orch
		}
		fmt.Fprintf(os.Stderr, "› provider=%s profile=%s (base=%s, delegate=%v)\n", prov.Name(), prof.Name, prof.BaseTier, prof.Delegate)
	} else if opts.Router != nil {
		fmt.Fprintf(os.Stderr, "› provider=%s routing=on (classify=%v escalate=%v)\n", prov.Name(), *classify, *escalate)
	} else {
		shown := *model
		if shown == "" {
			shown = provider.DefaultModel(*providerSlug)
		}
		fmt.Fprintf(os.Stderr, "› provider=%s model=%s\n", prov.Name(), shown)
	}

	// Append skill discovery to the orchestrator's system prompt (after a
	// profile may have set it). The load_skill tool is already in toolReg.
	if skillDiscovery != "" {
		if opts.System != "" {
			opts.System += "\n\n"
		}
		opts.System += skillDiscovery
	}

	// Memory: inject the identity's durable facts + the remember tool. Only for
	// an identity profile — a generic stateless run keeps no memory.
	if profileName != "" {
		memStore := memory.NewStore(profile.MemoryDir(profileName))
		mems, merr := memStore.Load()
		if merr != nil {
			fmt.Fprintln(os.Stderr, "warning: memory:", merr)
		}
		if mc := memory.Context(mems); mc != "" {
			if opts.System != "" {
				opts.System += "\n\n"
			}
			opts.System += mc
		}
		toolReg.Register(memory.NewRememberTool(memStore))

		// Self-improvement: let the identity write new skills it works out into
		// its own skills dir, so the procedure is reusable by name next time.
		toolReg.Register(skill.NewLearnTool(profile.SkillsDir(profileName)))
	}

	if err := agent.New(prov, toolReg, opts).Run(ctx, prompt, &cliHandler{}); err != nil {
		fmt.Fprintln(os.Stderr, "\nerror:", err)
		os.Exit(1)
	}
}

func modelsConfigPath() string {
	if v := os.Getenv("HARNESS_MODELS_FILE"); v != "" {
		return v
	}
	return "models.json"
}

// cliHandler renders the agent's stream to the terminal: assistant text on
// stdout, tool activity and accounting on stderr.
type cliHandler struct{}

func (cliHandler) OnText(delta string) { fmt.Print(delta) }

func (cliHandler) OnRoute(tier, model string) {
	fmt.Fprintf(os.Stderr, "  ↳ route: %s → %s\n", tier, model)
}

func (cliHandler) OnToolStart(name, id string) {
	fmt.Fprintf(os.Stderr, "\n  ⚙ %s …", name)
}

func (cliHandler) OnToolResult(name string, res tool.Result) {
	status := "ok"
	if res.IsError {
		status = "error"
	}
	fmt.Fprintf(os.Stderr, " %s (%d bytes)\n", status, len(res.Content))
}

func (cliHandler) OnUsage(u provider.Usage) {
	fmt.Fprintf(os.Stderr, "\n  ⟦ tokens in=%d out=%d cache_r=%d cache_w=%d ⟧",
		u.InputTokens, u.OutputTokens, u.CacheRead, u.CacheWrite)
}

func (cliHandler) OnStop(reason string) {
	fmt.Fprintf(os.Stderr, "\n  ■ stop: %s\n", reason)
}
