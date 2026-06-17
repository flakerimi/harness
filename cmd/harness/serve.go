package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/app"
	"github.com/flakerimi/harness/profile"
	"github.com/flakerimi/harness/server"
	"github.com/flakerimi/harness/session"
)

// runServe starts the HTTP+SSE server, so the same engine that powers the CLI
// can back a web UI, app, or chat channel — locally or deployed to a VPS the
// whole org connects to. Each request builds an agent for the requested
// identity (and optional provider/model) and streams the turn over SSE.
func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	providerSlug := fs.String("provider", "mock", "default model provider")
	model := fs.String("model", "", "explicit default model id")
	maxTokens := fs.Int("max-tokens", 4096, "max output tokens")
	root := fs.String("root", ".", "workspace root for filesystem tools")
	bash := fs.Bool("bash", false, "enable the bash tool (runs shell commands — trusted skills only)")
	compact := fs.Int("compact", 120000, "summarize older turns once estimated tokens exceed this (0 disables)")
	token := fs.String("token", "", "require this bearer token on /v1 (or $HARNESS_API_TOKEN; auto-generated if empty)")
	open := fs.Bool("open", false, "serve with NO auth (localhost dev only)")
	_ = fs.Parse(args)

	tok := resolveAPIToken(*token, *open)

	srv := &server.Server{
		DefaultProfile: activeProfile(),
		Token:          tok,
		Factory: func(ctx context.Context, profileName, provSlug, modelID string) (*agent.Agent, error) {
			if provSlug == "" {
				provSlug = *providerSlug
			}
			if modelID == "" {
				modelID = *model
			}
			return app.Build(ctx, app.Spec{
				Provider:  provSlug,
				Model:     modelID,
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
		Profiles: profileInfos,
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

	authMsg := "auth: token required"
	if tok == "" {
		authMsg = "auth: OPEN (no token — localhost only)"
	}
	fmt.Fprintf(os.Stderr, "harness serve · %s · default profile %q · %s\n", *addr, activeProfile(), authMsg)
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "stopped")
}

// profileInfos lists the identities for the API.
func profileInfos() []server.ProfileInfo {
	profs, _ := profile.List()
	out := make([]server.ProfileInfo, 0, len(profs))
	for _, p := range profs {
		out = append(out, server.ProfileInfo{Name: p.Name, Description: p.Description})
	}
	return out
}

// resolveAPIToken picks the API token: -open disables auth; else the flag, else
// $HARNESS_API_TOKEN, else a persisted token file (generated on first run).
func resolveAPIToken(flagTok string, open bool) string {
	if open {
		return ""
	}
	if flagTok != "" {
		return flagTok
	}
	if env := os.Getenv("HARNESS_API_TOKEN"); env != "" {
		return env
	}
	path := filepath.Join(profile.DataDir(""), "api-token")
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		return string(b)
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not generate token:", err)
		return ""
	}
	tok := hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		_ = os.WriteFile(path, []byte(tok), 0o600)
	}
	fmt.Fprintf(os.Stderr, "generated API token (saved to %s):\n  %s\n", path, tok)
	return tok
}
