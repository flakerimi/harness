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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/app"
	"github.com/flakerimi/harness/auth"
	"github.com/flakerimi/harness/channel/telegram"
	"github.com/flakerimi/harness/config"
	"github.com/flakerimi/harness/memory"
	"github.com/flakerimi/harness/profile"
	"github.com/flakerimi/harness/provider"
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

	ag, err := app.Build(ctx, app.Spec{
		Provider:  *providerSlug,
		Model:     *model,
		System:    *system,
		MaxTokens: *maxTokens,
		Root:      *root,
		Profile:   profileName,
		Tier:      *tierFlag,
		Route:     *route,
		Classify:  *classify,
		Escalate:  *escalate,
		Bash:      *bash,
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

	ag, err := app.Build(ctx, app.Spec{
		Provider:  *providerSlug,
		Model:     *model,
		System:    "You are a helpful assistant.",
		MaxTokens: *maxTokens,
		Root:      *root,
		Profile:   profileName,
		Tier:      *tierFlag,
		Route:     *route,
		Classify:  false, // chat holds a steady tier across the conversation
		Escalate:  *escalate,
		Bash:      *bash,
		Compact:   *compact,
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
	ag, err := app.Build(ctx, app.Spec{
		Provider:  provSlug,
		System:    "You are a helpful assistant.",
		MaxTokens: 4096,
		Root:      ".",
		Profile:   t.Profile,
		Tier:      "reasoning",
		Route:     true,
		Classify:  false,
		Escalate:  true,
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
	allow := fs.String("allow", "", "comma-separated Telegram chat ids allowed to use the bot (or $HARNESS_TELEGRAM_ALLOW); empty = open to anyone")
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

	allowSpec := *allow
	if allowSpec == "" {
		allowSpec = os.Getenv("HARNESS_TELEGRAM_ALLOW")
	}
	allowed := parseChatIDs(allowSpec)
	if len(allowed) == 0 {
		fmt.Fprintln(os.Stderr, "⚠ no -allow set: this bot is OPEN to anyone who finds it (they can spend your model credits)")
	} else {
		fmt.Fprintf(os.Stderr, "allowlist: %d chat(s) permitted\n", len(allowed))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	store := session.NewStore(profile.SessionsDir(profileName))
	bot := telegram.New(tok)

	// Advertise the slash commands in Telegram's "/" menu / autocomplete.
	if err := bot.SetCommands(ctx, []telegram.Command{
		{Command: "model", Description: "Switch model: /model <provider> [model]"},
		{Command: "models", Description: "List providers and show the current one"},
		{Command: "status", Description: "Show current model and conversation length"},
		{Command: "reset", Description: "Start this conversation fresh"},
		{Command: "help", Description: "Show available commands"},
	}); err != nil {
		fmt.Fprintln(os.Stderr, "warning: setMyCommands:", err)
	}

	responder := func(ctx context.Context, chatID int64, user, text string) string {
		// Allowlist gate: only permitted chats reach the model.
		if len(allowed) > 0 && !allowed[chatID] {
			fmt.Fprintf(os.Stderr, "blocked chat %d (%s): %q\n", chatID, user, clip(text, 60))
			return "🔒 This is a private assistant."
		}
		// /model (no args) and /models open the interactive provider→model menu.
		if isMenuCommand(text) {
			if err := bot.SendKeyboard(ctx, chatID, "Pick a provider:", providerMenuKeyboard()); err != nil {
				fmt.Fprintln(os.Stderr, "telegram: menu:", err)
			}
			return ""
		}

		sessID := "tg-" + strconv.FormatInt(chatID, 10)
		sess, err := store.Load(sessID)
		if err != nil {
			return "sorry — I couldn't load our conversation: " + err.Error()
		}

		// Other slash commands (/model kimi, /reset, …) are handled here and never
		// reach the model — they let you switch model, reset, etc. from the chat.
		if reply, isCmd := telegramCommand(store, sess, text, *providerSlug); isCmd {
			return reply
		}

		// Per-chat provider/model override (set via /model), else the launch flag.
		provSlug := firstNonEmpty(sess.Provider, *providerSlug)
		ag, err := app.Build(ctx, app.Spec{
			Provider:  provSlug,
			Model:     sess.Model,
			System:    "You are a helpful assistant replying over Telegram. Keep replies concise and chat-friendly.",
			MaxTokens: 4096,
			Root:      ".",
			Profile:   profileName,
			Tier:      "reasoning",
			Route:     true,
			Classify:  false,
			Escalate:  true,
			Compact:   *compact,
		})
		if err != nil {
			return "sorry — I hit a setup error: " + err.Error()
		}
		bh := &agent.Collector{}
		history, rerr := ag.Continue(ctx, sess.History, text, bh)
		sess.History = history
		if serr := store.Save(sess); serr != nil {
			fmt.Fprintln(os.Stderr, "warning: save:", serr)
		}
		out := strings.TrimSpace(bh.Text())
		if rerr != nil && out == "" {
			return "sorry — something went wrong: " + rerr.Error()
		}
		if out == "" {
			out = "(no reply)"
		}
		return out
	}

	// onCallback handles inline-keyboard taps from the /model menu.
	onCallback := func(ctx context.Context, chatID, messageID int64, callbackID, data, user string) {
		if len(allowed) > 0 && !allowed[chatID] {
			_ = bot.AnswerCallback(ctx, callbackID, "not allowed")
			return
		}
		switch {
		case data == "back":
			_ = bot.EditMessage(ctx, chatID, messageID, "Pick a provider:", providerMenuKeyboard())
			_ = bot.AnswerCallback(ctx, callbackID, "")
		case strings.HasPrefix(data, "p:"):
			slug := strings.TrimPrefix(data, "p:")
			_ = bot.EditMessage(ctx, chatID, messageID, "Model for "+slug+":", modelMenuKeyboard(slug))
			_ = bot.AnswerCallback(ctx, callbackID, "")
		case strings.HasPrefix(data, "s:"):
			slug, model, _ := strings.Cut(strings.TrimPrefix(data, "s:"), ":")
			sessID := "tg-" + strconv.FormatInt(chatID, 10)
			sess, err := store.Load(sessID)
			if err != nil {
				_ = bot.AnswerCallback(ctx, callbackID, "error")
				return
			}
			sess.Provider = slug
			sess.Model = model
			if serr := store.Save(sess); serr != nil {
				fmt.Fprintln(os.Stderr, "warning: save:", serr)
			}
			_ = bot.EditMessage(ctx, chatID, messageID, "✓ Now using "+slug+modelSuffix(model)+providerKeyWarning(slug), nil)
			_ = bot.AnswerCallback(ctx, callbackID, "switched to "+slug)
		default:
			_ = bot.AnswerCallback(ctx, callbackID, "")
		}
	}

	fmt.Fprintf(os.Stderr, "channel telegram · profile=%s · provider=%s — Ctrl-C to stop\n", profileName, *providerSlug)
	if err := bot.Run(ctx, responder, onCallback, func(e error) { fmt.Fprintln(os.Stderr, "telegram:", e) }); err != nil && err != context.Canceled {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "stopped")
}

// telegramCommand handles in-chat slash commands. It returns (reply, true) when
// the message is a command (which is then NOT sent to the model), persisting any
// change to the session. defProvider is the bot's launch-default provider.
func telegramCommand(store *session.Store, sess *session.Session, text, defProvider string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", false
	}
	// Normalize "/cmd@botname" (group mentions) → "cmd".
	cmd := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	cmd, _, _ = strings.Cut(cmd, "@")
	cur := firstNonEmpty(sess.Provider, defProvider)

	switch cmd {
	case "start", "help":
		return "I'm Morpheus, your assistant. Just talk to me normally. Commands:\n" +
			"/model <provider> [model] — switch model (e.g. /model kimi)\n" +
			"/models — list providers + show current\n" +
			"/status — current model and conversation length\n" +
			"/reset — start this conversation fresh\n\n" +
			"Currently: " + cur + modelSuffix(sess.Model), true

	case "models":
		return "Providers: " + strings.Join(provider.Slugs(), ", ") +
			"\nCurrent: " + cur + modelSuffix(sess.Model) +
			"\n\nSwitch with: /model <provider> [model]", true

	case "model":
		if len(fields) < 2 {
			return "usage: /model <provider> [model]\ne.g.  /model kimi   ·   /model deepseek deepseek-reasoner", true
		}
		slug := strings.ToLower(fields[1])
		if !slices.Contains(provider.Slugs(), slug) {
			return "unknown provider " + slug + ".\nProviders: " + strings.Join(provider.Slugs(), ", "), true
		}
		sess.Provider = slug
		sess.Model = ""
		if len(fields) >= 3 {
			sess.Model = fields[2]
		}
		if err := store.Save(sess); err != nil {
			return "couldn't save that: " + err.Error(), true
		}
		return "✓ switched to " + slug + modelSuffix(sess.Model) + providerKeyWarning(slug), true

	case "status", "whoami":
		return fmt.Sprintf("provider: %s%s\nprofile: telegram session %s\nturns: %d",
			cur, modelSuffix(sess.Model), sess.ID, sess.Turns()), true

	case "reset":
		sess.History = nil
		if err := store.Save(sess); err != nil {
			return "couldn't reset: " + err.Error(), true
		}
		return "✓ conversation reset (still on " + cur + modelSuffix(sess.Model) + ")", true

	default:
		return "unknown command /" + cmd + " — try /help", true
	}
}

// parseChatIDs parses a comma-separated list of Telegram chat ids into a set.
// Invalid entries are skipped.
func parseChatIDs(spec string) map[int64]bool {
	out := map[int64]bool{}
	for part := range strings.SplitSeq(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if id, err := strconv.ParseInt(part, 10, 64); err == nil {
			out[id] = true
		}
	}
	return out
}

func modelSuffix(model string) string {
	if model == "" {
		return ""
	}
	return " (" + model + ")"
}

// isMenuCommand reports whether a message should open the provider→model menu:
// "/models" or a bare "/model" (no provider argument).
func isMenuCommand(text string) bool {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return false
	}
	c := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	c, _, _ = strings.Cut(c, "@")
	return c == "models" || (c == "model" && len(fields) == 1)
}

// readyProviders lists providers usable right now for the menu: claude (OAuth)
// plus any provider with a stored API key.
func readyProviders() []string {
	out := []string{"claude"}
	cfg, _ := config.Load()
	var rest []string
	for slug, pc := range cfg.Providers {
		if slug != "claude" && pc.APIKey != "" {
			rest = append(rest, slug)
		}
	}
	slices.Sort(rest)
	return append(out, rest...)
}

// providerMenuKeyboard is the first menu level: a button per ready provider.
func providerMenuKeyboard() [][]telegram.Button {
	provs := readyProviders()
	var rows [][]telegram.Button
	var row []telegram.Button
	for i, p := range provs {
		row = append(row, telegram.Button{Text: p, CallbackData: "p:" + p})
		if len(row) == 3 || i == len(provs)-1 {
			rows = append(rows, row)
			row = nil
		}
	}
	return rows
}

// modelMenuKeyboard is the second level: the provider's default plus its curated
// models, then a back button. Selecting sends "s:<slug>:<model>" ("" = default).
func modelMenuKeyboard(slug string) [][]telegram.Button {
	rows := [][]telegram.Button{{{Text: "✓ default", CallbackData: "s:" + slug + ":"}}}
	for _, m := range provider.Models(slug) {
		rows = append(rows, []telegram.Button{{Text: m.Label, CallbackData: "s:" + slug + ":" + m.ID}})
	}
	rows = append(rows, []telegram.Button{{Text: "‹ back", CallbackData: "back"}})
	return rows
}

// providerKeyWarning reuses the real build logic to flag a provider that won't
// work yet (no key / missing endpoint), returning "" when it's good to go.
func providerKeyWarning(slug string) string {
	cfg, _ := config.Load()
	pc := cfg.ProviderConf(slug)
	if _, err := provider.BuildWith(slug, provider.BuildOptions{APIKey: pc.APIKey, BaseURL: pc.BaseURL}); err != nil {
		return "\n⚠ " + err.Error()
	}
	return ""
}

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
			return app.Build(ctx, app.Spec{
				Provider:  *providerSlug,
				Model:     *model,
				System:    "You are a helpful assistant.",
				MaxTokens: *maxTokens,
				Root:      *root,
				Profile:   profileName,
				Tier:      "reasoning",
				Route:     true,
				Classify:  false,
				Escalate:  true,
				Bash:      *bash,
				Compact:   *compact,
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
