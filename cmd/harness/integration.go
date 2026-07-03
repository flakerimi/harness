package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/flakerimi/harness/auth"
	"github.com/flakerimi/harness/channel/telegram"
	"github.com/flakerimi/harness/config"
	"github.com/flakerimi/harness/profile"
)

// integrationCommand handles /integration (and /connect) in a chat: for
// "google" it mints a remote-OAuth consent link bound to this chat's identity
// and sends it as a tappable button — sign in on the phone, the daemon's
// public callback saves the credential, done. Returns handled=false for
// non-integration messages.
func integrationCommand(ctx context.Context, bot *telegram.Bot, google *auth.GoogleRemote, chatID int64, profileName, text string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", false
	}
	cmd := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(fields[0], "/"), "@"+botCommandSuffix(fields[0])))
	if !strings.HasPrefix(fields[0], "/") || (cmd != "integration" && cmd != "connect") {
		return "", false
	}
	arg := ""
	if len(fields) > 1 {
		arg = strings.ToLower(fields[1])
	}
	switch arg {
	case "google":
		if google == nil {
			return "Google connect isn't configured on this server — it needs GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET (a Web OAuth client) and HARNESS_PUBLIC_URL.", true
		}
		authStore := auth.NewStore(profile.AuthFile(profileName))
		u, err := google.Start(authStore, "telegram:"+strconv.FormatInt(chatID, 10))
		if err != nil {
			return "couldn't start the Google connect: " + err.Error(), true
		}
		kb := [][]telegram.Button{{{Text: "🔗 Connect Google", URL: u}}}
		msg := fmt.Sprintf("Connecting Google to identity %q — tap the button, sign in, allow. The link is valid for 15 minutes.", profileName)
		if err := bot.SendKeyboard(ctx, chatID, msg, kb); err != nil {
			fmt.Fprintln(os.Stderr, "telegram: integration:", err)
			return "couldn't send the connect link: " + err.Error(), true
		}
		return "", true
	case "":
		return "usage: /integration google", true
	default:
		return fmt.Sprintf("unknown integration %q (available: google)", arg), true
	}
}

// botCommandSuffix extracts the @botname suffix from a command token, so
// "/integration@SomeBot" normalizes like "/integration".
func botCommandSuffix(token string) string {
	if _, after, ok := strings.Cut(token, "@"); ok {
		return after
	}
	return ""
}

// googleBrokerFromEnv builds the remote Google connect broker when a client
// (Web OAuth) and a public base URL are configured; nil otherwise.
func googleBrokerFromEnv() *auth.GoogleRemote {
	cfg, _ := config.Load()
	id, secret := cfg.GoogleClient()
	pub := strings.TrimRight(os.Getenv("HARNESS_PUBLIC_URL"), "/")
	if id == "" || secret == "" || pub == "" {
		return nil
	}
	return &auth.GoogleRemote{ClientID: id, ClientSecret: secret, RedirectURL: pub + "/oauth/google/callback"}
}

// notifyConnected pings the chat that started a remote connect once the
// callback lands — closing the loop without the user having to ask.
func notifyConnected(meta string) {
	kind, dest, ok := strings.Cut(meta, ":")
	if !ok || kind != "telegram" {
		return
	}
	tok := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID, err := strconv.ParseInt(dest, 10, 64)
	if tok == "" || err != nil {
		return
	}
	_ = telegram.New(tok).Send(context.Background(), chatID, "✓ Google connected — calendar and email are live for this identity.")
}
