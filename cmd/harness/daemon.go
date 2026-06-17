package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// runDaemon is the supervisor: it runs the HTTP API, the scheduler, and the
// enabled channels (Telegram) as goroutines under one signal context, with
// graceful shutdown. One durable process instead of three you hand-manage —
// the thing you deploy to a VPS.
func runDaemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "HTTP API listen address")
	providerSlug := fs.String("provider", "claude", "default model provider")
	model := fs.String("model", "", "default model id")
	maxTokens := fs.Int("max-tokens", 4096, "max output tokens")
	root := fs.String("root", ".", "workspace root for filesystem tools")
	bash := fs.Bool("bash", false, "enable the bash tool (trusted use only)")
	compact := fs.Int("compact", 120000, "compaction token budget (0 disables)")
	token := fs.String("token", "", "API bearer token (or $HARNESS_API_TOKEN; auto-generated)")
	open := fs.Bool("open", false, "API with NO auth (localhost only)")
	schedInterval := fs.Duration("schedule-interval", time.Minute, "how often the scheduler checks for due tasks")
	noSchedule := fs.Bool("no-schedule", false, "don't run the scheduler")
	telegramOn := fs.Bool("telegram", false, "also run the Telegram bot ($TELEGRAM_BOT_TOKEN)")
	tgProfile := fs.String("telegram-profile", "", "default identity for the Telegram bot")
	tgAllow := fs.String("allow", "", "Telegram allowlist (comma-separated chat ids)")
	printLaunchd := fs.Bool("print-launchd", false, "print a launchd plist for this daemon and exit")
	_ = fs.Parse(args)

	if *printLaunchd {
		printLaunchdPlist()
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	run := func(name string, fn func()) {
		wg.Go(func() {
			fn()
			fmt.Fprintf(os.Stderr, "daemon: %s stopped\n", name)
		})
	}

	// HTTP API (always on).
	tok := resolveAPIToken(*token, *open)
	srv := buildAPIServer(apiOptions{
		Provider: *providerSlug, Model: *model, MaxTokens: *maxTokens,
		Root: *root, Bash: *bash, Compact: *compact, Token: tok,
	})
	run("api", func() {
		fmt.Fprintf(os.Stderr, "daemon: api on %s\n", *addr)
		if err := serveHTTP(ctx, *addr, srv.Handler()); err != nil {
			fmt.Fprintln(os.Stderr, "daemon: api error:", err)
		}
	})

	// Scheduler (proactive tasks).
	if !*noSchedule {
		run("scheduler", func() { runScheduler(ctx, *schedInterval) })
	}

	// Telegram channel (optional).
	if *telegramOn {
		run("telegram", func() {
			if err := runTelegramBot(ctx, telegramOptions{
				Provider: *providerSlug, Profile: *tgProfile, Allow: *tgAllow, Compact: *compact,
			}); err != nil && err != context.Canceled {
				fmt.Fprintln(os.Stderr, "daemon: telegram:", err)
			}
		})
	}

	fmt.Fprintln(os.Stderr, "daemon: up — Ctrl-C / SIGTERM to stop")
	<-ctx.Done()
	fmt.Fprintln(os.Stderr, "\ndaemon: shutting down…")
	wg.Wait()
	fmt.Fprintln(os.Stderr, "daemon: stopped")
}

// printLaunchdPlist emits a launchd agent plist that runs this daemon at login
// and keeps it alive — for running the harness in the background on macOS.
func printLaunchdPlist() {
	bin, err := os.Executable()
	if err != nil || bin == "" {
		bin = "/usr/local/bin/harness"
	}
	fmt.Printf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>al.basecode.harness</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
    <string>-provider</string><string>claude</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HARNESS_API_TOKEN</key><string>REPLACE_WITH_YOUR_TOKEN</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/tmp/harness.log</string>
  <key>StandardErrorPath</key><string>/tmp/harness.err</string>
</dict>
</plist>
`, bin)
	fmt.Fprintln(os.Stderr, "\n# save to ~/Library/LaunchAgents/al.basecode.harness.plist, then:")
	fmt.Fprintln(os.Stderr, "#   launchctl load -w ~/Library/LaunchAgents/al.basecode.harness.plist")
}
