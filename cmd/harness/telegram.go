package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
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

// telegramOptions configures the bot loop, shared by the CLI and the daemon.
type telegramOptions struct {
	Token    string // bot token; empty falls back to $TELEGRAM_BOT_TOKEN
	Provider string // default model provider for chats that haven't picked one
	Profile  string // default identity; empty falls back to config
	Allow    string // comma-separated allowed chat ids; empty falls back to env
	Compact  int
}

// runTelegram is the CLI entry: parse flags, then run the bot until interrupted.
func runTelegram(args []string) {
	fs := flag.NewFlagSet("channel telegram", flag.ExitOnError)
	token := fs.String("token", "", "Telegram bot token (or $TELEGRAM_BOT_TOKEN)")
	providerSlug := fs.String("provider", "mock", "model provider slug")
	profileFlag := fs.String("profile", "", "default identity profile (default from config)")
	compact := fs.Int("compact", 120000, "summarize older turns once estimated tokens exceed this (0 disables)")
	allow := fs.String("allow", "", "comma-separated Telegram chat ids allowed to use the bot (or $HARNESS_TELEGRAM_ALLOW); empty = open to anyone")
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	err := runTelegramBot(ctx, telegramOptions{
		Token: *token, Provider: *providerSlug, Profile: *profileFlag, Allow: *allow, Compact: *compact,
	})
	if err != nil && err != context.Canceled {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "stopped")
}

// runTelegramBot builds and runs the bot loop until ctx is cancelled. It is the
// reusable core shared by the CLI and the daemon supervisor; it returns an error
// (e.g. no token) rather than exiting, so the daemon can skip it and keep going.
func runTelegramBot(ctx context.Context, o telegramOptions) error {
	tok := o.Token
	if tok == "" {
		tok = os.Getenv("TELEGRAM_BOT_TOKEN")
	}
	if tok == "" {
		return fmt.Errorf("telegram: no bot token (set -token or $TELEGRAM_BOT_TOKEN, from @BotFather)")
	}
	launchProvider := o.Provider
	if launchProvider == "" {
		launchProvider = "mock"
	}

	defaultProfile := o.Profile
	if defaultProfile == "" {
		cfg, _ := config.Load()
		defaultProfile = cfg.Profile()
	}

	allowSpec := o.Allow
	if allowSpec == "" {
		allowSpec = os.Getenv("HARNESS_TELEGRAM_ALLOW")
	}
	allowed := parseChatIDs(allowSpec)
	if len(allowed) == 0 {
		fmt.Fprintln(os.Stderr, "⚠ telegram: no allowlist — OPEN to anyone who finds the bot")
	} else {
		fmt.Fprintf(os.Stderr, "telegram: allowlist of %d chat(s)\n", len(allowed))
	}

	identities := newChatIdentities()
	storeFor := func(p string) *session.Store { return session.NewStore(profile.SessionsDir(p)) }
	bot := telegram.New(tok)

	// Advertise the slash commands in Telegram's "/" menu / autocomplete.
	if err := bot.SetCommands(ctx, []telegram.Command{
		{Command: "profile", Description: "Switch identity: /profile <name> (e.g. basecode)"},
		{Command: "profiles", Description: "List identities and show the current one"},
		{Command: "model", Description: "Switch model: /model <provider> [model]"},
		{Command: "models", Description: "Pick a provider/model from a menu"},
		{Command: "status", Description: "Show current identity, model, and length"},
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
		curProfile := identities.get(chatID, defaultProfile)

		// Identity switching (/profile, /profiles) — changes which identity (and
		// thus account/memory/thread) this chat uses.
		if reply, handled := identityCommand(identities, chatID, text, curProfile); handled {
			return reply
		}
		// /model (no args) and /models open the interactive provider→model menu.
		if isMenuCommand(text) {
			if err := bot.SendKeyboard(ctx, chatID, "Pick a provider:", providerMenuKeyboard()); err != nil {
				fmt.Fprintln(os.Stderr, "telegram: menu:", err)
			}
			return ""
		}

		store := storeFor(curProfile)
		sessID := "tg-" + strconv.FormatInt(chatID, 10)
		sess, err := store.Load(sessID)
		if err != nil {
			return "sorry — I couldn't load our conversation: " + err.Error()
		}

		// Model/reset/status/help commands operate on the current identity's session.
		if reply, isCmd := telegramCommand(store, sess, text, launchProvider, curProfile); isCmd {
			return reply
		}

		// Per-chat provider/model override (set via /model), else the launch flag.
		provSlug := firstNonEmpty(sess.Provider, launchProvider)
		ag, err := app.Build(ctx, app.Spec{
			Provider:  provSlug,
			Model:     sess.Model,
			System:    "You are a helpful assistant replying over Telegram. Keep replies concise and chat-friendly.",
			MaxTokens: 4096,
			Root:      ".",
			Profile:   curProfile,
			Tier:      "reasoning",
			Route:     true,
			Classify:  false,
			Escalate:  true,
			Compact:   o.Compact,
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

	// onCallback handles inline-keyboard taps from the /model menu, against the
	// chat's current identity.
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
			store := storeFor(identities.get(chatID, defaultProfile))
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

	fmt.Fprintf(os.Stderr, "telegram: bot up · default identity=%s · provider=%s\n", defaultProfile, launchProvider)
	return bot.Run(ctx, responder, onCallback, func(e error) { fmt.Fprintln(os.Stderr, "telegram:", e) })
}

// identityCommand handles /profile and /profiles — switching which identity a
// chat is talking to. Each identity has its own account, memory, and thread.
func identityCommand(ids *chatIdentities, chatID int64, text, cur string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", false
	}
	cmd := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	cmd, _, _ = strings.Cut(cmd, "@")

	switch cmd {
	case "profiles":
		return "Identities: " + strings.Join(profile.Names(), ", ") +
			"\nCurrent: " + cur +
			"\n\nSwitch with: /profile <name>", true
	case "profile":
		if len(fields) < 2 {
			return "usage: /profile <name>\nIdentities: " + strings.Join(profile.Names(), ", ") +
				"\nCurrent: " + cur, true
		}
		name := strings.ToLower(fields[1])
		if !slices.Contains(profile.Names(), name) {
			return "unknown identity " + name + ".\nIdentities: " + strings.Join(profile.Names(), ", "), true
		}
		if err := ids.set(chatID, name); err != nil {
			return "couldn't switch: " + err.Error(), true
		}
		return "✓ now acting as " + name + " — its own account, memory, and conversation.", true
	}
	return "", false
}

// telegramCommand handles model/reset/status/help. It returns (reply, true) when
// the message is one of those commands (which is then NOT sent to the model),
// persisting any change to the session.
func telegramCommand(store *session.Store, sess *session.Session, text, defProvider, curProfile string) (string, bool) {
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
			"/profile <name> — switch identity (e.g. basecode)\n" +
			"/profiles — list identities\n" +
			"/model <provider> [model] — switch model (e.g. /model kimi)\n" +
			"/models — pick from a menu\n" +
			"/status — current identity, model, and length\n" +
			"/reset — start this conversation fresh\n\n" +
			"Identity: " + curProfile + " · Model: " + cur + modelSuffix(sess.Model), true

	case "models":
		return "Providers: " + strings.Join(provider.Slugs(), ", ") +
			"\nCurrent: " + cur + modelSuffix(sess.Model) +
			"\n\nSwitch with: /model <provider> [model]", true

	case "model":
		if len(fields) < 2 {
			return "usage: /model <provider> [model]\ne.g.  /model kimi   ·   /model deepseek deepseek-v4-flash", true
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
		return fmt.Sprintf("identity: %s\nprovider: %s%s\nturns: %d",
			curProfile, cur, modelSuffix(sess.Model), sess.Turns()), true

	case "reset":
		sess.History = nil
		if err := store.Save(sess); err != nil {
			return "couldn't reset: " + err.Error(), true
		}
		return "✓ conversation reset (identity " + curProfile + ", model " + cur + modelSuffix(sess.Model) + ")", true

	default:
		return "unknown command /" + cmd + " — try /help", true
	}
}

// chatIdentities persists which identity each chat is currently using, so the
// choice survives restarts. It lives outside any single profile's dir (the
// pointer can't depend on the thing it selects).
type chatIdentities struct{ path string }

func newChatIdentities() *chatIdentities {
	return &chatIdentities{path: filepath.Join(profile.DataDir(""), "telegram", "identities.json")}
}

func (c *chatIdentities) load() map[string]string {
	m := map[string]string{}
	if raw, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

func (c *chatIdentities) get(chatID int64, def string) string {
	if p := c.load()[strconv.FormatInt(chatID, 10)]; p != "" {
		return p
	}
	return def
}

func (c *chatIdentities) set(chatID int64, profileName string) error {
	m := c.load()
	m[strconv.FormatInt(chatID, 10)] = profileName
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(c.path, raw, 0o600)
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
