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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/auth"
	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/session"
	"github.com/flakerimi/harness/task"
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
	Tasks          *task.Store // background-task queue; nil disables /v1/tasks
	DefaultProfile string
	Token          string // when set, every /v1 request must present it (Bearer header or ?token=)

	// Public, when set, serves that directory's files at the server root —
	// UNAUTHENTICATED. It's how the assistant publishes: write_file to the
	// workspace's pub/ dir → the page is live at /<path>. Point it at a
	// dedicated pub dir, never the whole workspace.
	Public string

	// GoogleOAuth, when set, enables the public /oauth/google/callback route —
	// the landing spot for the remote connect flow (consent link handed out in
	// chat, browser on the user's phone). OnConnected, if set, is told the
	// meta of a finished connect (e.g. "telegram:<chatID>") so the surface
	// that started it can confirm.
	GoogleOAuth *auth.GoogleRemote
	OnConnected func(meta string)

	// Ready, when set, gates /healthz: a non-nil error turns it into a 503.
	// The daemon wires its egress watchdog here, so a container whose outbound
	// network has wedged (inbound keeps working — that's this exact failure
	// mode) reports unhealthy and the supervisor's health check restarts it.
	Ready func() error
}

// Handler returns the HTTP routes, with CORS applied and /v1 token-gated.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if s.Ready != nil {
			if err := s.Ready(); err != nil {
				http.Error(w, "unhealthy: "+err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /", s.handleIndex)
	mux.Handle("POST /v1/chat", s.guard(s.handleChat))
	mux.Handle("GET /v1/profiles", s.guard(s.handleProfiles))
	mux.Handle("GET /v1/models", s.guard(s.handleModels))
	mux.Handle("GET /v1/sessions", s.guard(s.handleSessions))
	mux.Handle("GET /v1/sessions/{id}", s.guard(s.handleSessionHistory))
	if s.Tasks != nil {
		mux.Handle("GET /v1/tasks", s.guard(s.handleTasks))
		mux.Handle("POST /v1/tasks", s.guard(s.handleTaskAdd))
		mux.Handle("GET /v1/tasks/{id}", s.guard(s.handleTaskShow))
	}
	if s.GoogleOAuth != nil {
		mux.HandleFunc("GET /oauth/google/callback", s.handleGoogleCallback)
	}
	return cors(mux)
}

// handleGoogleCallback is where Google's consent redirect lands (the user's
// browser, so it must be open). The one-time state minted by Start is the
// authorization; a bad or replayed state fails.
func (s *Server) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	state, code := r.URL.Query().Get("state"), r.URL.Query().Get("code")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		fmt.Fprintf(w, oauthPage, "✗ Google connect failed", "Google said: "+errMsg)
		return
	}
	if state == "" || code == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, oauthPage, "✗ Invalid callback", "Missing state or code — start the connect again.")
		return
	}
	meta, err := s.GoogleOAuth.Finish(r.Context(), state, code)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, oauthPage, "✗ Google connect failed", err.Error())
		return
	}
	fmt.Fprintf(w, oauthPage, "✓ Google connected", "You can close this tab and go back to the chat.")
	if s.OnConnected != nil {
		s.OnConnected(meta)
	}
}

const oauthPage = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>harness</title><body style="font-family:system-ui;display:grid;place-items:center;min-height:80vh;text-align:center">
<div><h1 style="font-size:1.6rem">%s</h1><p style="color:#555">%s</p></div>`

// handleIndex answers "/" with the service card; any other unmatched GET is
// tried against the Public dir (published pages), 404 otherwise. http.Dir
// refuses path traversal, so serving stays confined to Public.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		if s.Public != "" {
			http.FileServer(http.Dir(s.Public)).ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"service":   "harness",
		"auth":      s.Token != "",
		"endpoints": []string{"POST /v1/chat", "GET /v1/profiles", "GET /v1/models", "GET /v1/sessions", "GET /v1/sessions/{id}", "GET /v1/tasks", "POST /v1/tasks", "GET /v1/tasks/{id}", "GET /healthz"},
	})
}

// handleTasks lists the background-task queue, newest last (store order).
func (s *Server) handleTasks(w http.ResponseWriter, _ *http.Request) {
	all, err := s.Tasks.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if all == nil {
		all = []task.Task{}
	}
	writeJSON(w, http.StatusOK, all)
}

// handleTaskAdd enqueues a background job; the daemon's worker executes it.
func (s *Server) handleTaskAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile  string `json:"profile"`
		Prompt   string `json:"prompt"`
		Provider string `json:"provider"`
		Deliver  string `json:"deliver"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.Profile == "" {
		req.Profile = s.DefaultProfile
	}
	t, err := s.Tasks.Enqueue(task.Task{Profile: req.Profile, Provider: req.Provider, Prompt: req.Prompt, Deliver: req.Deliver})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleTaskShow(w http.ResponseWriter, r *http.Request) {
	t, err := s.Tasks.Get(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	writeJSON(w, http.StatusOK, t)
}

type chatRequest struct {
	Profile  string      `json:"profile"`
	Session  string      `json:"session"`
	Message  string      `json:"message"`
	Provider string      `json:"provider"` // optional: switch this session's model provider
	Model    string      `json:"model"`    // optional: pin this session's model
	Images   []chatImage `json:"images"`   // optional: photos for vision-capable models
}

// chatImage is one inbound image on a chat turn, base64-encoded so it rides
// plain JSON. Blind models degrade it to a placeholder at the provider seam.
type chatImage struct {
	MediaType string `json:"media_type"` // e.g. "image/jpeg", "image/png"
	Data      string `json:"data"`       // base64 (std encoding) image bytes
}

// maxChatImageBytes caps a decoded image. 5 MB is the tightest per-image limit
// among vision providers — same cap the Telegram surface applies.
const maxChatImageBytes = 5 << 20

// content decodes the request into user-turn blocks: images first, then the
// text. An error names the offending image so the client can fix it.
func (c chatRequest) content() ([]provider.Block, error) {
	blocks := make([]provider.Block, 0, len(c.Images)+1)
	for i, im := range c.Images {
		raw, err := base64.StdEncoding.DecodeString(im.Data)
		if err != nil {
			return nil, fmt.Errorf("images[%d]: invalid base64", i)
		}
		if len(raw) > maxChatImageBytes {
			return nil, fmt.Errorf("images[%d]: %d bytes exceeds the %d MB limit", i, len(raw), maxChatImageBytes>>20)
		}
		if im.MediaType == "" {
			return nil, fmt.Errorf("images[%d]: media_type is required", i)
		}
		blocks = append(blocks, provider.Block{
			Type:  provider.BlockImage,
			Image: &provider.ImageBlock{MediaType: im.MediaType, Data: raw},
		})
	}
	if strings.TrimSpace(c.Message) != "" {
		blocks = append(blocks, provider.Block{Type: provider.BlockText, Text: c.Message})
	}
	return blocks, nil
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.Message == "" && len(req.Images) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message or images required"})
		return
	}
	content, err := req.content()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
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

	history, runErr := ag.ContinueWith(r.Context(), sess.History, content, h)
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
