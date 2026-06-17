// Package server exposes the harness over HTTP with Server-Sent Events, so the
// same engine that powers the CLI can drive a web UI, a mobile app, or a chat
// channel. It is transport-only: the agent, its tools, and per-profile session
// persistence are injected, keeping this package free of wiring decisions and
// reusable as a library.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/session"
	"github.com/flakerimi/harness/tool"
)

// Server adapts the agent engine to HTTP. Factory builds an agent for a given
// identity; Sessions resolves that identity's conversation store. Both are
// injected by the caller (the CLI wires in its full profile/skill/memory
// build), so this package stays decoupled from configuration.
type Server struct {
	Factory        func(ctx context.Context, profile string) (*agent.Agent, error)
	Sessions       func(profile string) *session.Store
	DefaultProfile string
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("POST /v1/chat", s.handleChat)
	mux.HandleFunc("GET /v1/sessions", s.handleSessions)
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "harness",
		"endpoints": map[string]string{
			"POST /v1/chat":    "stream a chat turn (SSE); body {profile, session, message}",
			"GET /v1/sessions": "list a profile's conversations; ?profile=",
			"GET /healthz":     "liveness check",
		},
	})
}

type chatRequest struct {
	Profile string `json:"profile"`
	Session string `json:"session"`
	Message string `json:"message"`
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
	prof := req.Profile
	if prof == "" {
		prof = s.DefaultProfile
	}
	sessID := req.Session
	if sessID == "" {
		sessID = "default"
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}

	ag, err := s.Factory(r.Context(), prof)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	store := s.Sessions(prof)
	sess, err := store.Load(sessID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	h := &sseHandler{w: w, flusher: flusher}
	h.send("ready", map[string]string{"profile": prof, "session": sess.ID})

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

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	prof := r.URL.Query().Get("profile")
	if prof == "" {
		prof = s.DefaultProfile
	}
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
