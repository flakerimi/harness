// Package server exposes the harness over HTTP with Server-Sent Events, so the
// same engine that powers the CLI can drive a web UI, a desktop or mobile app,
// or a chat channel — including a shared instance deployed to a VPS that an org
// connects to. It is transport-only: the agent, its tools, and per-profile
// session persistence are injected, keeping this package free of wiring
// decisions and reusable as a library.
//
// When deployed remotely it sits behind an HTTPS reverse proxy (the proxy
// terminates TLS); set Token so every /v1 request must authenticate, and CORS
// is permitted (token-gated) so browser and desktop clients on other origins
// can connect.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/session"
	"github.com/flakerimi/harness/tool"
)

// ProfileInfo is one identity advertised to clients.
type ProfileInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Server adapts the agent engine to HTTP. Factory builds an agent for a given
// identity + provider/model; Sessions resolves that identity's conversation
// store; Profiles lists the identities. All are injected by the caller, so this
// package stays decoupled from configuration.
type Server struct {
	Factory        func(ctx context.Context, profile, providerSlug, model string) (*agent.Agent, error)
	Sessions       func(profile string) *session.Store
	Profiles       func() []ProfileInfo
	DefaultProfile string
	Token          string // when set, every /v1 request must present it (Bearer header or ?token=)
}

// Handler returns the HTTP routes, with CORS applied and /v1 token-gated.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /", s.handleIndex)
	mux.Handle("POST /v1/chat", s.guard(s.handleChat))
	mux.Handle("GET /v1/profiles", s.guard(s.handleProfiles))
	mux.Handle("GET /v1/models", s.guard(s.handleModels))
	mux.Handle("GET /v1/sessions", s.guard(s.handleSessions))
	mux.Handle("GET /v1/sessions/{id}", s.guard(s.handleSessionHistory))
	return cors(mux)
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service":   "harness",
		"auth":      s.Token != "",
		"endpoints": []string{"POST /v1/chat", "GET /v1/profiles", "GET /v1/models", "GET /v1/sessions", "GET /v1/sessions/{id}", "GET /healthz"},
	})
}

type chatRequest struct {
	Profile  string `json:"profile"`
	Session  string `json:"session"`
	Message  string `json:"message"`
	Provider string `json:"provider"` // optional: switch this session's model provider
	Model    string `json:"model"`    // optional: pin this session's model
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message is required"})
		return
	}
	prof := orDefault(req.Profile, s.DefaultProfile)
	sessID := orDefault(req.Session, "default")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}

	store := s.Sessions(prof)
	sess, err := store.Load(sessID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Optional per-session provider/model override from the client.
	if req.Provider != "" {
		sess.Provider = req.Provider
		sess.Model = req.Model // a provider switch resets the model unless one is given
	} else if req.Model != "" {
		sess.Model = req.Model
	}

	ag, err := s.Factory(r.Context(), prof, sess.Provider, sess.Model)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	h := &sseHandler{w: w, flusher: flusher}
	h.send("ready", map[string]string{"profile": prof, "session": sess.ID, "provider": sess.Provider, "model": sess.Model})

	history, runErr := ag.Continue(r.Context(), sess.History, req.Message, h)
	sess.History = history
	if serr := store.Save(sess); serr != nil {
		h.send("error", map[string]string{"error": "save: " + serr.Error()})
	}
	if runErr != nil {
		h.send("error", map[string]string{"error": runErr.Error()})
	}
	h.send("done", map[string]any{"turns": sess.Turns()})
}

func (s *Server) handleProfiles(w http.ResponseWriter, _ *http.Request) {
	var profs []ProfileInfo
	if s.Profiles != nil {
		profs = s.Profiles()
	}
	writeJSON(w, http.StatusOK, map[string]any{"default": s.DefaultProfile, "profiles": profs})
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	out := make([]map[string]any, 0)
	for _, slug := range provider.Slugs() {
		models := make([]map[string]string, 0)
		for _, m := range provider.Models(slug) {
			models = append(models, map[string]string{"label": m.Label, "id": m.ID})
		}
		out = append(out, map[string]any{
			"provider": slug,
			"default":  provider.DefaultModel(slug),
			"models":   models,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	prof := orDefault(r.URL.Query().Get("profile"), s.DefaultProfile)
	metas, err := s.Sessions(prof).List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(metas))
	for _, m := range metas {
		out = append(out, map[string]any{"id": m.ID, "turns": m.Turns})
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": prof, "sessions": out})
}

func (s *Server) handleSessionHistory(w http.ResponseWriter, r *http.Request) {
	prof := orDefault(r.URL.Query().Get("profile"), s.DefaultProfile)
	sess, err := s.Sessions(prof).Load(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"profile":  prof,
		"id":       sess.ID,
		"provider": sess.Provider,
		"model":    sess.Model,
		"turns":    sess.Turns(),
		"messages": sess.History,
	})
}

// guard enforces token auth on a handler when a token is configured.
func (s *Server) guard(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" && !s.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		h(w, r)
	})
}

// authorized accepts the token via an Authorization: Bearer header or a ?token=
// query param (the latter for EventSource, which can't set headers).
func (s *Server) authorized(r *http.Request) bool {
	tok := []byte(s.Token)
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(h, "Bearer ")), tok) == 1 {
			return true
		}
	}
	if q := r.URL.Query().Get("token"); q != "" {
		return subtle.ConstantTimeCompare([]byte(q), tok) == 1
	}
	return false
}

// cors permits cross-origin clients (browser/desktop apps on other origins).
// Safe with "*" because the API is token-gated, not cookie-authenticated.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sseHandler implements agent.Handler (and RouteAware), writing each event as an
// SSE frame and flushing so the client streams in real time.
type sseHandler struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func (h *sseHandler) send(event string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(h.w, "event: %s\ndata: %s\n\n", event, payload)
	h.flusher.Flush()
}

func (h *sseHandler) OnText(delta string) { h.send("text", map[string]string{"delta": delta}) }
func (h *sseHandler) OnToolStart(name, id string) {
	h.send("tool_start", map[string]string{"name": name, "id": id})
}
func (h *sseHandler) OnToolResult(name string, res tool.Result) {
	h.send("tool_result", map[string]any{"name": name, "content": res.Content, "is_error": res.IsError})
}
func (h *sseHandler) OnUsage(u provider.Usage) { h.send("usage", u) }
func (h *sseHandler) OnStop(reason string)     { h.send("stop", map[string]string{"reason": reason}) }
func (h *sseHandler) OnRoute(tier, model string) {
	h.send("route", map[string]string{"tier": tier, "model": model})
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
