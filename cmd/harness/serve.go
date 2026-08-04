package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/app"
	"github.com/flakerimi/harness/auth"
	"github.com/flakerimi/harness/channel/apns"
	"github.com/flakerimi/harness/inbox"
	"github.com/flakerimi/harness/memory"
	"github.com/flakerimi/harness/profile"
	"github.com/flakerimi/harness/schedule"
	"github.com/flakerimi/harness/server"
	"github.com/flakerimi/harness/session"
	"github.com/flakerimi/harness/task"
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
	root := fs.String("root", "", "filesystem root for tools (default: the profile's workspace)")
	bash := fs.Bool("bash", false, "enable the bash tool (runs shell commands — trusted skills only)")
	compact := fs.Int("compact", 120000, "summarize older turns once estimated tokens exceed this (0 disables)")
	token := fs.String("token", "", "require this bearer token on /v1 (or $HARNESS_API_TOKEN; auto-generated if empty)")
	open := fs.Bool("open", false, "serve with NO auth (localhost dev only)")
	_ = fs.Parse(args)

	tok := resolveAPIToken(*token, *open)
	srv := buildAPIServer(apiOptions{
		Provider: *providerSlug, Model: *model, MaxTokens: *maxTokens,
		Root: *root, Bash: *bash, Compact: *compact, Token: tok,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	authMsg := "auth: token required"
	if tok == "" {
		authMsg = "auth: OPEN (no token — localhost only)"
	}
	fmt.Fprintf(os.Stderr, "harness serve · %s · default profile %q · %s\n", *addr, activeProfile(), authMsg)
	if err := serveHTTP(ctx, *addr, srv.Handler()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "stopped")
}

// apiOptions configures the HTTP API server, shared by serve and the daemon.
type apiOptions struct {
	Provider  string
	Model     string
	MaxTokens int
	Root      string
	Bash      bool
	Compact   int
	Token     string
}

// buildAPIServer wires the HTTP API (agent factory + session store + profiles).
func buildAPIServer(o apiOptions) *server.Server {
	srv := &server.Server{
		DefaultProfile: activeProfile(),
		Token:          o.Token,
		Tasks:          task.NewStore(profile.TasksDir()),
		Factory: func(ctx context.Context, profileName, provSlug, modelID string) (*agent.Agent, error) {
			if provSlug == "" {
				provSlug = o.Provider
			}
			if modelID == "" {
				modelID = o.Model
			}
			return app.Build(ctx, app.Spec{
				Provider:  provSlug,
				Model:     modelID,
				System:    "You are a helpful assistant.",
				MaxTokens: o.MaxTokens,
				Root:      o.Root,
				Profile:   profileName,
				Tier:      "reasoning",
				Route:     true,
				// Chat turns vary wildly; classify each one so a quick lookup
				// rides the fast tier instead of always paying reasoning.
				Classify: true,
				Escalate: true,
				Bash:     o.Bash,
				Compact:  o.Compact,
				MaxTurns: 40,
				// Background work queued from an app chat reports back to the
				// identity's own inbox + push banner.
				TaskDeliver: "app:" + profileName,
			})
		},
		Sessions: func(profileName string) *session.Store {
			return session.NewStore(profile.SessionsDir(profileName))
		},
		Profiles: profileInfos,
		// Device-token registration for push delivery (`push:<profile>`); the
		// tokens live in the identity's data dir like everything else it owns.
		PushRegister: func(profileName, token, platform string) error {
			return apns.NewTokenStore(profile.DataDir(profileName)).Add(token, platform)
		},
		// Integration status + the credential store the Google connect-start
		// endpoint binds consent links to — the app's Settings page drives the
		// same remote OAuth flow the chat surfaces use.
		Connectors: func(ctx context.Context, profileName string) []server.ConnectorInfo {
			var out []server.ConnectorInfo
			for _, c := range app.Connectors(false, profileName).Connectors() {
				st := c.Status(ctx)
				out = append(out, server.ConnectorInfo{Name: c.Name(), Connected: st.Connected, Detail: st.Detail})
				if cl, ok := c.(interface{ Close() error }); ok {
					_ = cl.Close()
				}
			}
			// Claude subscription auth: connected when OAuth credentials are on
			// file (a refresh token keeps a stale access token usable).
			claude := server.ConnectorInfo{Name: "claude", Detail: "connect your Claude subscription"}
			if creds, err := auth.NewStore(profile.AuthFile(profileName)).Load("claude"); err == nil {
				if creds.Refresh != "" || time.Now().UnixMilli() < creds.Expires {
					claude.Connected = true
					claude.Detail = "Claude subscription connected"
				} else {
					claude.Detail = "session expired — reconnect"
				}
			}
			return append(out, claude)
		},
		AuthStore: func(profileName string) *auth.Store {
			return auth.NewStore(profile.AuthFile(profileName))
		},
		// The app-side archive of scheduled runs and task results.
		Inbox: func(profileName string) *inbox.Store {
			return inbox.NewStore(profile.DataDir(profileName))
		},
		// The proactive clock, read-only — the app's home screen shows what
		// runs and when.
		Schedules: func() ([]schedule.Task, error) {
			return schedule.NewStore(profile.ScheduleDir()).Load()
		},
		// 👎 corrections become durable lessons — the same loop the Telegram
		// feedback buttons ran, now first-class over HTTP.
		Feedback: func(profileName, lesson string) error {
			fb := memory.NewFeedbackTool(memory.NewStore(profile.MemoryDir(profileName)))
			in, _ := json.Marshal(map[string]string{"lesson": lesson})
			res, err := fb.Run(context.Background(), in, nil)
			if err != nil {
				return err
			}
			if res.IsError {
				return fmt.Errorf("%s", res.Content)
			}
			return nil
		},
	}
	// Published pages: the default identity's workspace pub/ dir is served at
	// the root — how the assistant shares a long report as a link instead of a
	// wall of chat text.
	if p := activeProfile(); p != "" {
		srv.Public = filepath.Join(profile.WorkspaceDir(p), "pub")
	}
	return srv
}

// serveHTTP runs an HTTP server until ctx is cancelled, then shuts it down
// gracefully. Reusable by serve and the daemon supervisor.
func serveHTTP(ctx context.Context, addr string, h http.Handler) error {
	// Header/idle timeouts guard against slow-loris and dead keep-alives; no
	// WriteTimeout — it would cut long SSE turn streams mid-reply.
	hs := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutCtx)
	}()
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
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
