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
	"github.com/flakerimi/harness/connector"
	"github.com/flakerimi/harness/connector/mcp"
	"github.com/flakerimi/harness/profile"
	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/router"
	"github.com/flakerimi/harness/tool"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "login":
			runLogin(os.Args[2:])
			return
		case "connectors":
			runConnectors(os.Args[2:])
			return
		}
	}
	runAgent(os.Args[1:])
}

// defaultConnectors wires the integrations available to this harness instance.
// Native built-ins are always present; external connectors (calendar, mail,
// search via MCP) are added here as they're built — never hardcoded deeper in.
func defaultConnectors() *connector.Registry {
	r := connector.NewRegistry()
	r.Add(connector.NewNative("builtin", tool.ReadFile{}, tool.WebFetch{}, tool.WebSearch{}))

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

	for _, c := range defaultConnectors().Connectors() {
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

func runLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	prov := fs.String("provider", "claude", "provider to log in (claude)")
	authFile := fs.String("auth-file", authFileDefault(), "credential file to write")
	urlOnly := fs.Bool("url-only", false, "print the authorize URL and exit (no browser, no wait)")
	_ = fs.Parse(args)

	switch strings.ToLower(*prov) {
	case "claude", "anthropic":
	default:
		fmt.Fprintf(os.Stderr, "login: only 'claude' is supported (got %q)\n", *prov)
		os.Exit(2)
	}

	if *urlOnly {
		u, _, err := auth.AnthropicAuthURL()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println(u)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	store := auth.NewStore(*authFile)
	fmt.Fprintln(os.Stderr, "Opening browser for Claude login…")
	if _, err := auth.AnthropicLogin(ctx, store, "claude", func(u string) {
		fmt.Fprintln(os.Stderr, "If your browser didn't open, visit:\n  "+u)
	}); err != nil {
		fmt.Fprintln(os.Stderr, "login failed:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "✓ logged in — credentials saved to %s\n", *authFile)
}

func runAgent(args []string) {
	fs := flag.NewFlagSet("harness", flag.ExitOnError)
	providerSlug := fs.String("provider", "mock", "provider slug: mock | anthropic|claude | openai | deepseek | ollama | lmstudio")
	model := fs.String("model", "", "explicit model id — overrides automatic routing")
	system := fs.String("system", "You are a helpful assistant.", "system prompt")
	maxTokens := fs.Int("max-tokens", 4096, "max output tokens")
	root := fs.String("root", ".", "workspace root for filesystem tools")
	profileFlag := fs.String("profile", "", "agent profile (e.g. meeting-prep); empty = generic assistant")
	tierFlag := fs.String("tier", "reasoning", "base routing tier: fast | balanced | reasoning")
	route := fs.Bool("route", true, "automatic model routing (ignored when -model is set)")
	classify := fs.Bool("classify", true, "classify task difficulty to pick the base tier")
	escalate := fs.Bool("escalate", true, "escalate a tier when a turn produces nothing usable")
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

	prov, err := provider.Build(*providerSlug)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	reg, err := defaultConnectors().Tools(ctx)
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

	// Routing is on when requested, or implied by a profile.
	var rt *router.Table
	if (*model == "" && *route) || *profileFlag != "" {
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

	if *profileFlag != "" {
		prof, ok := profile.Get(*profileFlag)
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown profile %q (available: %s)\n", *profileFlag, strings.Join(profile.Names(), ", "))
			os.Exit(2)
		}
		opts.System = prof.Persona
		opts.BaseTier = prof.BaseTier
		opts.Classify = false // the profile sets the orchestrator's tier
		if prof.Delegate {
			orch := tool.NewRegistry()
			for _, t := range reg.All() {
				orch.Register(t)
			}
			orch.Register(agent.Delegate{
				Provider:  prov,
				Tools:     reg, // worker uses the connector tools (no delegate → no recursion)
				Router:    rt,
				Tier:      prof.WorkerTier,
				System:    prof.WorkerPersona,
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
