// Command harness is a thin reference CLI built on the framework. Subcommands:
//
//	harness login [-provider claude]   # OAuth login → writes auth.json
//	harness [flags] <prompt>           # run one agent turn (default)
//
// It exists to dogfood the library — the real product is the packages it imports.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/auth"
	"github.com/flakerimi/harness/channel/telegram"
	"github.com/flakerimi/harness/config"
	"github.com/flakerimi/harness/connector"
	"github.com/flakerimi/harness/connector/google"
	"github.com/flakerimi/harness/connector/mcp"
	"github.com/flakerimi/harness/memory"
	"github.com/flakerimi/harness/profile"
	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/router"
	"github.com/flakerimi/harness/schedule"
	"github.com/flakerimi/harness/server"
	"github.com/flakerimi/harness/session"
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
		case "chat":
			runChat(os.Args[2:])
			return
		case "sessions":
			runSessions(os.Args[2:])
			return
		case "schedule":
			runSchedule(os.Args[2:])
			return
		case "serve":
			runServe(os.Args[2:])
			return
		case "channel":
			runChannel(os.Args[2:])
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	ag, err := buildAgent(ctx, agentSpec{
		providerSlug: *providerSlug,
		model:        *model,
		system:       *system,
		maxTokens:    *maxTokens,
		root:         *root,
		profileName:  profileName,
		tier:         *tierFlag,
		route:        *route,
		classify:     *classify,
		escalate:     *escalate,
		bash:         *bash,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if err := ag.Run(ctx, prompt, &cliHandler{}); err != nil {
		fmt.Fprintln(os.Stderr, "\nerror:", err)
		os.Exit(1)
	}
}

// agentSpec is the resolved configuration for building an Agent — shared by the
// one-shot run path and the interactive chat path.
type agentSpec struct {
	providerSlug  string
	model         string
	system        string
	maxTokens     int
	root          string
	profileName   string
	tier          string
	route         bool
	classify      bool
	escalate      bool
	bash          bool
	compactTokens int // 0 disables summarizing compaction (one-shot runs)
}

// buildAgent wires a provider, connector tools, skills, routing, profile
// persona, delegation, and memory into a ready-to-run Agent. It is the single
// place that assembles the full assistant, so `run` and `chat` behave
// identically.
func buildAgent(ctx context.Context, spec agentSpec) (*agent.Agent, error) {
	// Resolve provider credentials/endpoint from config (env still overrides),
	// so stored keys work without exporting env vars.
	cfg, _ := config.Load()
	pc := cfg.ProviderConf(spec.providerSlug)
	prov, err := provider.BuildWith(spec.providerSlug, provider.BuildOptions{APIKey: pc.APIKey, BaseURL: pc.BaseURL})
	if err != nil {
		return nil, err
	}

	reg, err := defaultConnectors(spec.bash, spec.profileName).Tools(ctx)
	if err != nil {
		return nil, err
	}

	// Model precedence: explicit -model, else a config-pinned model for this
	// provider (needed for OpenAI-compatible providers with no built-in default).
	model := spec.model
	if model == "" {
		model = pc.Model
	}

	caps := []string{provider.CapTools, provider.CapCaching}
	opts := agent.Options{
		Model:         model,
		System:        spec.system,
		MaxTokens:     spec.maxTokens,
		Caps:          caps,
		Env:           &tool.Env{Root: spec.root},
		CompactTokens: spec.compactTokens,
	}
	toolReg := reg // tools the orchestrator uses

	// Agent Skills: register the load_skill tool into reg so both the
	// orchestrator and any delegated workers get it, and build the discovery
	// text that advertises skills in the system prompt. For an identity profile,
	// its own learned-skills dir is scanned first (and wins on name conflicts).
	var skillDirs []string
	if spec.profileName != "" {
		skillDirs = append(skillDirs, profile.SkillsDir(spec.profileName))
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
	if (spec.model == "" && spec.route) || spec.profileName != "" {
		t, rerr := router.LoadTable(modelsConfigPath())
		if rerr != nil {
			fmt.Fprintln(os.Stderr, "warning: models config:", rerr)
			t = router.DefaultTable()
		}
		rt = t
		opts.Router = rt
		opts.BaseTier = router.ParseTier(spec.tier, router.TierReasoning)
		opts.Classify = spec.classify
		opts.Escalate = spec.escalate
	}

	if spec.profileName != "" {
		prof, ok := profile.Get(spec.profileName)
		if !ok {
			return nil, fmt.Errorf("unknown profile %q (available: %s)", spec.profileName, strings.Join(profile.Names(), ", "))
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
				MaxTokens: spec.maxTokens,
				Caps:      caps,
			})
			toolReg = orch
		}
		fmt.Fprintf(os.Stderr, "› provider=%s profile=%s (base=%s, delegate=%v)\n", prov.Name(), prof.Name, prof.BaseTier, prof.Delegate)
	} else if opts.Router != nil {
		fmt.Fprintf(os.Stderr, "› provider=%s routing=on (classify=%v escalate=%v)\n", prov.Name(), spec.classify, spec.escalate)
	} else {
		shown := spec.model
		if shown == "" {
			shown = provider.DefaultModel(spec.providerSlug)
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
	if spec.profileName != "" {
		memStore := memory.NewStore(profile.MemoryDir(spec.profileName))
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
		toolReg.Register(skill.NewLearnTool(profile.SkillsDir(spec.profileName)))
	}

	return agent.New(prov, toolReg, opts), nil
}

// runChat is the interactive multi-turn conversation. It loads (or starts) a
// persisted session for the active identity, then loops: read a line, run a turn
// with the full prior history, stream the reply, and save. The conversation
// survives across invocations — the assistant remembers what was said.
func runChat(args []string) {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	providerSlug := fs.String("provider", "mock", "provider slug: mock | anthropic|claude | openai | deepseek | gemini | ollama | lmstudio")
	model := fs.String("model", "", "explicit model id — overrides automatic routing")
	maxTokens := fs.Int("max-tokens", 4096, "max output tokens")
	root := fs.String("root", ".", "workspace root for filesystem tools")
	profileFlag := fs.String("profile", "", "identity profile (e.g. personal, work); default from config")
	tierFlag := fs.String("tier", "reasoning", "base routing tier: fast | balanced | reasoning")
	route := fs.Bool("route", true, "automatic model routing (ignored when -model is set)")
	escalate := fs.Bool("escalate", true, "escalate a tier when a turn produces nothing usable")
	bash := fs.Bool("bash", false, "enable the bash tool (runs shell commands — trusted skills only)")
	sessionID := fs.String("session", "default", "conversation id (scoped to the profile)")
	reset := fs.Bool("new", false, "start this session fresh, discarding prior history")
	compact := fs.Int("compact", 120000, "summarize older turns once the chat's estimated tokens exceed this (0 disables)")
	_ = fs.Parse(args)

	profileName := *profileFlag
	if profileName == "" {
		cfg, _ := config.Load()
		profileName = cfg.Profile()
	}

	store := session.NewStore(profile.SessionsDir(profileName))
	if *reset {
		if err := store.Reset(*sessionID); err != nil {
			fmt.Fprintln(os.Stderr, "warning: reset:", err)
		}
	}
	sess, err := store.Load(*sessionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	ag, err := buildAgent(ctx, agentSpec{
		providerSlug:  *providerSlug,
		model:         *model,
		system:        "You are a helpful assistant.",
		maxTokens:     *maxTokens,
		root:          *root,
		profileName:   profileName,
		tier:          *tierFlag,
		route:         *route,
		classify:      false, // chat holds a steady tier across the conversation
		escalate:      *escalate,
		bash:          *bash,
		compactTokens: *compact,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	label := profileName
	if label == "" {
		label = "(default)"
	}
	fmt.Fprintf(os.Stderr, "chat: %s · session %q · %d prior turns — /exit to quit, /reset to clear\n", label, sess.ID, sess.Turns())

	scan := bufio.NewScanner(os.Stdin)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	h := &cliHandler{}
	for {
		fmt.Print("\nyou › ")
		if !scan.Scan() {
			fmt.Println()
			break // EOF (Ctrl-D)
		}
		line := strings.TrimSpace(scan.Text())
		switch line {
		case "":
			continue
		case "/exit", "/quit":
			return
		case "/reset":
			_ = store.Reset(sess.ID)
			sess = &session.Session{ID: sess.ID}
			fmt.Fprintln(os.Stderr, "(conversation reset)")
			continue
		}

		fmt.Print("\nasst › ")
		history, rerr := ag.Continue(ctx, sess.History, line, h)
		fmt.Println()
		sess.History = history // keep partial history even on error
		if serr := store.Save(sess); serr != nil {
			fmt.Fprintln(os.Stderr, "warning: save:", serr)
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return // interrupted
			}
			fmt.Fprintln(os.Stderr, "error:", rerr)
		}
	}
}

// runSessions lists the active identity's stored conversations.
func runSessions(args []string) {
	fs := flag.NewFlagSet("sessions", flag.ExitOnError)
	profileFlag := fs.String("profile", "", "identity profile (default from config)")
	_ = fs.Parse(args)
	name := *profileFlag
	if name == "" {
		name = activeProfile()
	}
	store := session.NewStore(profile.SessionsDir(name))
	metas, err := store.List()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	label := name
	if label == "" {
		label = "(default)"
	}
	fmt.Printf("sessions for %q  [%s]\n", label, store.Dir())
	for _, m := range metas {
		fmt.Printf("- %-16s %d turns\n", m.ID, m.Turns)
	}
	if len(metas) == 0 {
		fmt.Println("(no conversations yet — start one with `harness chat`)")
	}
}

// runSchedule is the proactive surface: register, inspect, and fire scheduled
// tasks (each an identity + prompt + schedule). `run-due` is meant to be invoked
// by a system cron/launchd; `daemon` keeps a process alive checking on an
// interval; `run` fires one task immediately.
func runSchedule(args []string) {
	store := schedule.NewStore(profile.ScheduleDir())
	sub := ""
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "add":
		scheduleAdd(store, args)
	case "list", "ls", "":
		scheduleList(store)
	case "remove", "rm":
		scheduleRemove(store, args)
	case "run":
		scheduleRun(store, args)
	case "run-due":
		scheduleRunDue(store)
	case "daemon":
		scheduleDaemon(store, args)
	default:
		fmt.Fprintf(os.Stderr, "unknown schedule subcommand %q\nusage: harness schedule <add|list|remove|run|run-due|daemon>\n", sub)
		os.Exit(2)
	}
}

func scheduleAdd(store *schedule.Store, args []string) {
	fs := flag.NewFlagSet("schedule add", flag.ExitOnError)
	profileFlag := fs.String("profile", "", "identity profile to run as (default from config)")
	spec := fs.String("spec", "", "schedule: 'every 30m' | 'daily 08:00' | 'weekly mon 09:00'")
	id := fs.String("id", "", "optional task id (auto-assigned if empty)")
	providerSlug := fs.String("provider", "mock", "model provider to run this task with")
	_ = fs.Parse(args)

	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if *spec == "" || prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: harness schedule add -spec <spec> [-profile p] [-provider claude] [-id name] <prompt>")
		os.Exit(2)
	}
	prof := *profileFlag
	if prof == "" {
		prof = activeProfile()
	}
	t, err := store.Add(schedule.Task{
		ID:       *id,
		Profile:  prof,
		Provider: *providerSlug,
		Prompt:   prompt,
		Spec:     *spec,
	}, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("added %s · profile=%s · %s · next %s\n", t.ID, t.Profile, t.Spec, t.NextRun.Format("Mon 2006-01-02 15:04"))
}

func scheduleList(store *schedule.Store) {
	tasks, err := store.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("scheduled tasks  [%s]\n", store.Dir())
	for _, t := range tasks {
		state := "on"
		if !t.Enabled {
			state = "off"
		}
		last := "never"
		if !t.LastRun.IsZero() {
			last = t.LastRun.Format("01-02 15:04")
		}
		fmt.Printf("- %-10s [%s] profile=%s spec=%q next=%s last=%s\n      %s\n",
			t.ID, state, t.Profile, t.Spec, t.NextRun.Format("01-02 15:04"), last, clip(t.Prompt, 80))
	}
	if len(tasks) == 0 {
		fmt.Println("(no tasks — add one with `harness schedule add`)")
	}
}

func scheduleRemove(store *schedule.Store, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: harness schedule remove <id>")
		os.Exit(2)
	}
	ok, err := store.Remove(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if !ok {
		fmt.Printf("no task %q\n", args[0])
		return
	}
	fmt.Printf("removed %s\n", args[0])
}

func scheduleRun(store *schedule.Store, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: harness schedule run <id>")
		os.Exit(2)
	}
	tasks, err := store.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	var task *schedule.Task
	for i := range tasks {
		if tasks[i].ID == args[0] {
			task = &tasks[i]
		}
	}
	if task == nil {
		fmt.Fprintf(os.Stderr, "no task %q\n", args[0])
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	runScheduledTask(ctx, *task)
	if err := store.MarkRan(task.ID, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
}

func scheduleRunDue(store *schedule.Store) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if n := runDueOnce(ctx, store, time.Now()); n == 0 {
		fmt.Fprintln(os.Stderr, "(nothing due)")
	}
}

func scheduleDaemon(store *schedule.Store, args []string) {
	fs := flag.NewFlagSet("schedule daemon", flag.ExitOnError)
	interval := fs.Duration("interval", time.Minute, "how often to check for due tasks")
	_ = fs.Parse(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Fprintf(os.Stderr, "schedule daemon: checking every %s — Ctrl-C to stop\n", *interval)
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		runDueOnce(ctx, store, time.Now())
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "\nstopped")
			return
		case <-ticker.C:
		}
	}
}

// runDueOnce fires every task due at now and records each run. It returns how
// many tasks ran.
func runDueOnce(ctx context.Context, store *schedule.Store, now time.Time) int {
	due, err := store.Due(now)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 0
	}
	for _, t := range due {
		if ctx.Err() != nil {
			break
		}
		runScheduledTask(ctx, t)
		if err := store.MarkRan(t.ID, time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, "warning: mark ran:", err)
		}
	}
	return len(due)
}

// runScheduledTask builds the task's identity and runs its prompt once,
// streaming the result to stdout (so a system cron captures it).
func runScheduledTask(ctx context.Context, t schedule.Task) {
	provSlug := t.Provider
	if provSlug == "" {
		provSlug = "mock"
	}
	fmt.Printf("\n──[ %s · profile=%s · %s ]──\n", t.ID, t.Profile, time.Now().Format("2006-01-02 15:04"))
	ag, err := buildAgent(ctx, agentSpec{
		providerSlug: provSlug,
		system:       "You are a helpful assistant.",
		maxTokens:    4096,
		root:         ".",
		profileName:  t.Profile,
		tier:         "reasoning",
		route:        true,
		classify:     false,
		escalate:     true,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return
	}
	if err := ag.Run(ctx, t.Prompt, &cliHandler{}); err != nil {
		fmt.Fprintln(os.Stderr, "\nerror:", err)
	}
	fmt.Println()
}

// clip shortens s to n runes with an ellipsis, for compact listings.
func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// runChannel dispatches chat-channel subcommands (Telegram for now), bridging
// an inbound chat to the assistant.
func runChannel(args []string) {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "telegram":
		runTelegram(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown channel %q\nusage: harness channel telegram -token <t> [-profile p] [-provider claude]\n", sub)
		os.Exit(2)
	}
}

// runTelegram runs the Telegram bot loop: each inbound message resumes that
// chat's own session (so the conversation has memory), runs a turn, and replies.
func runTelegram(args []string) {
	fs := flag.NewFlagSet("channel telegram", flag.ExitOnError)
	token := fs.String("token", "", "Telegram bot token (or $TELEGRAM_BOT_TOKEN)")
	providerSlug := fs.String("provider", "mock", "model provider slug")
	profileFlag := fs.String("profile", "", "identity profile (default from config)")
	compact := fs.Int("compact", 120000, "summarize older turns once estimated tokens exceed this (0 disables)")
	_ = fs.Parse(args)

	tok := *token
	if tok == "" {
		tok = os.Getenv("TELEGRAM_BOT_TOKEN")
	}
	if tok == "" {
		fmt.Fprintln(os.Stderr, "a bot token is required: -token <t> or $TELEGRAM_BOT_TOKEN (create one via @BotFather)")
		os.Exit(2)
	}

	profileName := *profileFlag
	if profileName == "" {
		cfg, _ := config.Load()
		profileName = cfg.Profile()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	store := session.NewStore(profile.SessionsDir(profileName))
	bot := telegram.New(tok)

	responder := func(ctx context.Context, chatID int64, user, text string) string {
		sessID := "tg-" + strconv.FormatInt(chatID, 10)
		sess, err := store.Load(sessID)
		if err != nil {
			return "sorry — I couldn't load our conversation: " + err.Error()
		}
		ag, err := buildAgent(ctx, agentSpec{
			providerSlug:  *providerSlug,
			system:        "You are a helpful assistant replying over Telegram. Keep replies concise and chat-friendly.",
			maxTokens:     4096,
			root:          ".",
			profileName:   profileName,
			tier:          "reasoning",
			route:         true,
			classify:      false,
			escalate:      true,
			compactTokens: *compact,
		})
		if err != nil {
			return "sorry — I hit a setup error: " + err.Error()
		}
		bh := &bufHandler{}
		history, rerr := ag.Continue(ctx, sess.History, text, bh)
		sess.History = history
		if serr := store.Save(sess); serr != nil {
			fmt.Fprintln(os.Stderr, "warning: save:", serr)
		}
		out := strings.TrimSpace(bh.text.String())
		if rerr != nil && out == "" {
			return "sorry — something went wrong: " + rerr.Error()
		}
		if out == "" {
			out = "(no reply)"
		}
		return out
	}

	fmt.Fprintf(os.Stderr, "channel telegram · profile=%s · provider=%s — Ctrl-C to stop\n", profileName, *providerSlug)
	if err := bot.Run(ctx, responder, func(e error) { fmt.Fprintln(os.Stderr, "telegram:", e) }); err != nil && err != context.Canceled {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "stopped")
}

// bufHandler collects the agent's streamed text into a buffer — for surfaces
// (channels) that send one complete reply rather than streaming deltas.
type bufHandler struct{ text strings.Builder }

func (h *bufHandler) OnText(delta string)                  { h.text.WriteString(delta) }
func (h *bufHandler) OnToolStart(_, _ string)              {}
func (h *bufHandler) OnToolResult(_ string, _ tool.Result) {}
func (h *bufHandler) OnUsage(_ provider.Usage)             {}
func (h *bufHandler) OnStop(_ string)                      {}

// runServe starts the HTTP+SSE server, so the same engine that powers the CLI
// can back a web UI, app, or chat channel. Each request builds an agent for the
// requested identity via the shared buildAgent and streams the turn over SSE.
func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	providerSlug := fs.String("provider", "mock", "provider slug: mock | anthropic|claude | openai | deepseek | gemini | ollama | lmstudio")
	model := fs.String("model", "", "explicit model id — overrides automatic routing")
	maxTokens := fs.Int("max-tokens", 4096, "max output tokens")
	root := fs.String("root", ".", "workspace root for filesystem tools")
	bash := fs.Bool("bash", false, "enable the bash tool (runs shell commands — trusted skills only)")
	compact := fs.Int("compact", 120000, "summarize older turns once estimated tokens exceed this (0 disables)")
	_ = fs.Parse(args)

	srv := &server.Server{
		DefaultProfile: activeProfile(),
		Factory: func(ctx context.Context, profileName string) (*agent.Agent, error) {
			return buildAgent(ctx, agentSpec{
				providerSlug:  *providerSlug,
				model:         *model,
				system:        "You are a helpful assistant.",
				maxTokens:     *maxTokens,
				root:          *root,
				profileName:   profileName,
				tier:          "reasoning",
				route:         true,
				classify:      false,
				escalate:      true,
				bash:          *bash,
				compactTokens: *compact,
			})
		},
		Sessions: func(profileName string) *session.Store {
			return session.NewStore(profile.SessionsDir(profileName))
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	hs := &http.Server{Addr: *addr, Handler: srv.Handler()}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutCtx)
	}()

	fmt.Fprintf(os.Stderr, "harness serve · %s · default profile %q\n", *addr, activeProfile())
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "stopped")
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
