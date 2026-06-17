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

	"github.com/flakerimi/harness/app"
	"github.com/flakerimi/harness/config"
	"github.com/flakerimi/harness/provider"
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
		case "agents":
			runAgents(os.Args[2:])
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
		case "onboard":
			runOnboard(os.Args[2:])
			return
		}
	}
	runAgent(os.Args[1:])
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

// clip shortens s to n runes with an ellipsis, for compact listings.
func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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
