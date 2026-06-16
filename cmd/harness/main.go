// Command harness is a thin reference CLI built on the framework: pick a
// provider, register tools, run one agent turn, stream the result. It exists
// to dogfood the library — the real product is the packages it imports.
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
	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/tool"
)

func main() {
	providerSlug := flag.String("provider", "mock", "provider slug: mock | anthropic|claude | openai | ollama | lmstudio")
	model := flag.String("model", "", "model id (defaults per provider)")
	system := flag.String("system", "You are a helpful assistant.", "system prompt")
	maxTokens := flag.Int("max-tokens", 4096, "max output tokens")
	root := flag.String("root", ".", "workspace root for filesystem tools")
	flag.Parse()

	prompt := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if prompt == "" {
		data, _ := io.ReadAll(os.Stdin)
		prompt = strings.TrimSpace(string(data))
	}
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: harness [flags] <prompt>   (or pipe the prompt on stdin)")
		flag.PrintDefaults()
		os.Exit(2)
	}

	prov, err := provider.Build(*providerSlug)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	modelID := *model
	if modelID == "" {
		modelID = provider.DefaultModel(*providerSlug)
	}

	reg := tool.NewRegistry()
	reg.Register(tool.ReadFile{})

	ag := agent.New(prov, reg, agent.Options{
		Model:     modelID,
		System:    *system,
		MaxTokens: *maxTokens,
		Caps:      []string{provider.CapTools, provider.CapCaching},
		Env:       &tool.Env{Root: *root},
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Fprintf(os.Stderr, "› provider=%s model=%s\n", prov.Name(), modelID)
	if err := ag.Run(ctx, prompt, &cliHandler{}); err != nil {
		fmt.Fprintln(os.Stderr, "\nerror:", err)
		os.Exit(1)
	}
}

// cliHandler renders the agent's stream to the terminal: assistant text on
// stdout, tool activity and accounting on stderr.
type cliHandler struct{}

func (cliHandler) OnText(delta string) { fmt.Print(delta) }

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
