package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/flakerimi/harness/app"
	"github.com/flakerimi/harness/config"
	"github.com/flakerimi/harness/profile"
	"github.com/flakerimi/harness/session"
)

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
