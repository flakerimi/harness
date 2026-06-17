package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/app"
	"github.com/flakerimi/harness/channel/telegram"
	"github.com/flakerimi/harness/config"
	"github.com/flakerimi/harness/profile"
	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/session"
)

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
