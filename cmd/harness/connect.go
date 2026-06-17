package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/flakerimi/harness/auth"
	"github.com/flakerimi/harness/config"
	"github.com/flakerimi/harness/profile"
)

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
