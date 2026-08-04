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
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/auth"
	"github.com/flakerimi/harness/inbox"
	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/schedule"
	"github.com/flakerimi/harness/session"
	"github.com/flakerimi/harness/task"
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

	// PushRegister, when set, enables POST /v1/push/register — a client app
	// stores its APNs device token against an identity so `push:<profile>`
	// deliveries reach it. Nil returns 501, so the endpoint honestly reports
	// that push isn't wired rather than silently swallowing tokens.
	PushRegister func(profile, token, platform string) error

	// Connectors, when set, enables GET /v1/connectors — integration status for
	// a client app's settings page. AuthStore names the profile's credential
	// store so POST /v1/connectors/google/connect can mint a consent link bound
	// to it (the same GoogleOAuth flow the chat surfaces use).
	Connectors func(ctx context.Context, profile string) []ConnectorInfo
	AuthStore  func(profile string) *auth.Store

	// Inbox, when set, enables the deliveries feed — the app-side archive of
	// scheduled runs and task results (`app:<profile>` deliveries land here).
	Inbox func(profile string) *inbox.Store

	// Feedback, when set, enables POST /v1/feedback — a client's 👎 correction
	// becomes a durable lesson in the identity's memory, the same loop the
	// Telegram buttons ran.
	Feedback func(profile, lesson string) error

	// Schedules, when set, enables GET /v1/schedules — the identity's proactive
	// clock, read-only, so a client's home screen can show what runs and when.
	Schedules func() ([]schedule.Task, error)

	// TurnTimeout bounds a detached turn's lifetime (default 10 minutes) and
	// MaxTurns caps how many run at once across all sessions (default 4). Turns
	// outlive their HTTP connections — a phone backgrounding mid-reply must not
	// kill the run — so these are the only things that stop one.
	TurnTimeout time.Duration
	MaxTurns    int

	// RatePerMin caps /v1 requests per bearer token (or client IP when
	// anonymous). Zero means the default 60; negative disables limiting.
	RatePerMin int

	// Titler, when set, names a session after its first exchange (≤6 words) —
	// the Claude-app sidebar convention. It runs inside the turn goroutine
	// right before the save (single writer, no racing re-save) with a short
	// timeout; failures just leave the fallback first-message title.
	Titler func(ctx context.Context, profile, transcript string) (string, error)

	turnsOnce sync.Once
	turnMgr   *turnManager

	limitOnce sync.Once
	limiter   *rateLimiter

	claudeOnce sync.Once
	claudeFlow *claudeAuth
}

// turns lazily builds the manager so a zero-value Server keeps working in
// tests and embedders that never touch chat.
func (s *Server) turns() *turnManager {
	s.turnsOnce.Do(func() {
		timeout := s.TurnTimeout
		if timeout == 0 {
			timeout = 10 * time.Minute
		}
		max := s.MaxTurns
		if max <= 0 {
			max = 4 // zero AND negative mean "default", never "reject all"
		}
		s.turnMgr = newTurnManager(max, timeout)
	})
	return s.turnMgr
}

func (s *Server) handleSchedules(w http.ResponseWriter, _ *http.Request) {
	if s.Schedules == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "schedules not configured"})
		return
	}
	tasks, err := s.Schedules()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Dashboard order: what fires next first; disabled tasks sink; one-shots
	// that already fired (no next run) sit between.
	sort.SliceStable(tasks, func(i, j int) bool {
		a, b := tasks[i], tasks[j]
		if a.Enabled != b.Enabled {
			return a.Enabled
		}
		if a.NextRun.IsZero() != b.NextRun.IsZero() {
			return !a.NextRun.IsZero()
		}
		return a.NextRun.Before(b.NextRun)
	})
	if tasks == nil {
		tasks = []schedule.Task{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": tasks})
}

func (s *Server) handleDeliveries(w http.ResponseWriter, r *http.Request) {
	if s.Inbox == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "deliveries not configured"})
		return
	}
	prof := orDefault(r.URL.Query().Get("profile"), s.DefaultProfile)
	store := s.Inbox(prof)
	items := store.List()
	// Newest first for clients; the store keeps oldest-first on disk.
	out := make([]inbox.Item, 0, len(items))
	for i := len(items) - 1; i >= 0; i-- {
		out = append(out, items[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": prof, "unread": store.Unread(), "items": out})
}

func (s *Server) handleDeliveriesRead(w http.ResponseWriter, r *http.Request) {
	if s.Inbox == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "deliveries not configured"})
		return
	}
	var req struct {
		Profile string   `json:"profile"`
		IDs     []string `json:"ids"` // empty = mark everything read
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	prof := orDefault(req.Profile, s.DefaultProfile)
	if err := s.Inbox(prof).MarkRead(req.IDs...); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"profile": prof, "status": "ok"})
}

func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	if s.Feedback == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "feedback not configured"})
		return
	}
	var req struct {
		Profile string `json:"profile"`
		Lesson  string `json:"lesson"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Lesson) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "lesson is required"})
		return
	}
	prof := orDefault(req.Profile, s.DefaultProfile)
	if err := s.Feedback(prof, strings.TrimSpace(req.Lesson)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"profile": prof, "status": "saved"})
}

// ConnectorInfo is one integration's live status, as shown to clients.
type ConnectorInfo struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	Detail    string `json:"detail"`
}

func (s *Server) handleConnectors(w http.ResponseWriter, r *http.Request) {
	if s.Connectors == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "connector listing not configured"})
		return
	}
	prof := orDefault(r.URL.Query().Get("profile"), s.DefaultProfile)
	writeJSON(w, http.StatusOK, map[string]any{"profile": prof, "connectors": s.Connectors(r.Context(), prof)})
}

func (s *Server) handleGoogleConnectStart(w http.ResponseWriter, r *http.Request) {
	if s.GoogleOAuth == nil || s.AuthStore == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "remote google connect not configured (needs GOOGLE_CLIENT_ID/SECRET and a public URL)"})
		return
	}
	var req struct {
		Profile string `json:"profile"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req) // empty body = default profile
	prof := orDefault(req.Profile, s.DefaultProfile)
	u, err := s.GoogleOAuth.Start(s.AuthStore(prof), "api:"+prof)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"profile": prof, "url": u})
}

func (s *Server) handlePushRegister(w http.ResponseWriter, r *http.Request) {
	if s.PushRegister == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "push registration not configured"})
		return
	}
	var req struct {
		Profile  string `json:"profile"`
		Token    string `json:"token"`
		Platform string `json:"platform"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Token) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token is required"})
		return
	}
	prof := orDefault(req.Profile, s.DefaultProfile)
	if err := s.PushRegister(prof, strings.TrimSpace(req.Token), req.Platform); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"profile": prof, "status": "registered"})
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
	mux.Handle("POST /v1/chat", s.guardMax(s.handleChat, 12<<20)) // images ride the chat body
	mux.Handle("GET /v1/chat/stream", s.guard(s.handleChatStream))
	mux.Handle("GET /v1/profiles", s.guard(s.handleProfiles))
	mux.Handle("GET /v1/models", s.guard(s.handleModels))
	mux.Handle("GET /v1/sessions", s.guard(s.handleSessions))
	mux.Handle("GET /v1/sessions/{id}", s.guard(s.handleSessionHistory))
	mux.Handle("DELETE /v1/sessions/{id}", s.guard(s.handleSessionDelete))
	mux.Handle("POST /v1/push/register", s.guard(s.handlePushRegister))
	mux.Handle("GET /v1/connectors", s.guard(s.handleConnectors))
	mux.Handle("POST /v1/connectors/google/connect", s.guard(s.handleGoogleConnectStart))
	mux.Handle("POST /v1/auth/claude/start", s.guard(s.handleClaudeAuthStart))
	mux.Handle("POST /v1/auth/claude/complete", s.guard(s.handleClaudeAuthComplete))
	mux.Handle("GET /v1/deliveries", s.guard(s.handleDeliveries))
	mux.Handle("POST /v1/deliveries/read", s.guard(s.handleDeliveriesRead))
	mux.Handle("POST /v1/feedback", s.guard(s.handleFeedback))
	mux.Handle("GET /v1/schedules", s.guard(s.handleSchedules))
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
		"endpoints": []string{"POST /v1/chat", "GET /v1/chat/stream", "GET /v1/profiles", "GET /v1/models", "GET /v1/sessions", "GET /v1/sessions/{id}", "GET /v1/tasks", "POST /v1/tasks", "GET /v1/tasks/{id}", "GET /healthz"},
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
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": fmt.Sprintf("body exceeds the %d MB limit", tooBig.Limit>>20)})
			return
		}
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

	key := prof + "/" + sessID
	startSeq, err := s.turns().Start(key, func(ctx context.Context, h agent.Handler) {
		jh := h.(*journalHandler)
		// The agent is built INSIDE the turn on the turn's own context: its
		// timeout-bound cancel is what reaps connector subprocesses (MCP
		// servers spawn via exec.CommandContext) when the turn ends. A factory
		// failure surfaces as an SSE error frame rather than an HTTP status —
		// the turn already exists by then.
		ag, ferr := s.Factory(ctx, prof, sess.Provider, sess.Model)
		if ferr != nil {
			jh.send("error", map[string]string{"error": ferr.Error()})
			jh.send("done", map[string]any{"turns": sess.Turns()})
			return
		}
		jh.send("ready", map[string]string{"profile": prof, "session": sess.ID, "provider": sess.Provider, "model": sess.Model})
		history, runErr := ag.ContinueWith(ctx, sess.History, content, h)
		sess.History = history
		if runErr == nil {
			s.titleSession(ctx, prof, sess) // never title a failed exchange
		}
		if serr := store.Save(sess); serr != nil {
			jh.send("error", map[string]string{"error": "save: " + serr.Error()})
		}
		if runErr != nil {
			jh.send("error", map[string]string{"error": runErr.Error()})
		}
		jh.send("done", map[string]any{"turns": sess.Turns()})
	})
	switch err {
	case errTurnBusy:
		latest, _ := s.turns().Running(key)
		writeJSON(w, http.StatusConflict, map[string]any{"error": "turn in progress", "seq": latest})
		return
	case errTurnCapacity:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	s.streamFrom(w, r, flusher, key, startSeq)
}

// titleSession fills in a session's title after its first exchange. It runs in
// the turn goroutine before the save so there is exactly one writer; a slow or
// failing Titler costs at most the timeout and leaves the fallback title.
func (s *Server) titleSession(ctx context.Context, prof string, sess *session.Session) {
	if s.Titler == nil || sess.Title != "" || sess.Turns() != 1 {
		return
	}
	transcript := session.Transcript(sess.History)
	if len(transcript) > 2000 {
		transcript = transcript[:2000]
	}
	tctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	title, err := s.Titler(tctx, prof, transcript)
	if err != nil {
		return
	}
	if title = strings.TrimSpace(strings.Trim(strings.TrimSpace(title), `"`)); title != "" {
		sess.Title = title
	}
}

// streamFrom subscribes to a session's turn and writes SSE until the turn ends
// or the client goes away. A client disconnect never cancels the turn — the
// client re-attaches later via GET /v1/chat/stream with its last seen id.
func (s *Server) streamFrom(w http.ResponseWriter, r *http.Request, flusher http.Flusher, key string, after uint64) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	replay, evicted, live, cancel, _ := s.turns().Subscribe(key, after)
	defer cancel()
	writeFrame := func(f frame) {
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", f.Seq, f.Event, f.Data)
	}
	if evicted {
		// The resume cursor predates the journal window: tell the client to
		// refetch session history before trusting the replay.
		fmt.Fprint(w, "event: reset\ndata: {}\n\n")
	}
	for _, f := range replay {
		writeFrame(f)
	}
	flusher.Flush()
	if live == nil {
		return
	}
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case f, ok := <-live:
			if !ok {
				return
			}
			writeFrame(f)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleChatStream re-attaches a client to a session's turn: journal replay
// after the given cursor, then live frames until the turn ends.
func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	prof := orDefault(r.URL.Query().Get("profile"), s.DefaultProfile)
	sessID := orDefault(r.URL.Query().Get("session"), "default")
	cursor := r.URL.Query().Get("after")
	if cursor == "" {
		// EventSource auto-reconnects carry the cursor as a header instead.
		cursor = r.Header.Get("Last-Event-ID")
	}
	after, _ := strconv.ParseUint(cursor, 10, 64)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	s.streamFrom(w, r, flusher, prof+"/"+sessID, after)
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
	// Conversations list: most recently touched first, like any chat app.
	sort.Slice(metas, func(i, j int) bool { return metas[i].Updated.After(metas[j].Updated) })
	out := make([]map[string]any, 0, len(metas))
	for _, m := range metas {
		out = append(out, map[string]any{
			"id": m.ID, "turns": m.Turns,
			"title": m.Title, "preview": m.Preview,
			"updated": m.Updated.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": prof, "sessions": out})
}

// handleSessionDelete removes a conversation — swipe-to-delete in the app.
func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	prof := orDefault(r.URL.Query().Get("profile"), s.DefaultProfile)
	if err := s.Sessions(prof).Reset(r.PathValue("id")); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"profile": prof, "status": "deleted"})
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
func (s *Server) guard(h http.HandlerFunc) http.Handler { return s.guardMax(h, 1<<20) }

// guardMax token-gates a route, rate-limits it, and caps its request body.
// Chat takes 12 MB (images); everything else 1 MB.
func (s *Server) guardMax(h http.HandlerFunc, maxBody int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" && !s.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if !s.allow(r) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limited — slow down"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		h(w, r)
	})
}

// allow charges the request against its caller's bucket: the bearer token (or
// ?token= — EventSource can't set headers) when present, else the remote host.
// The ephemeral port must not be part of the key, or reconnecting would mint
// fresh buckets and bypass the limit.
func (s *Server) allow(r *http.Request) bool {
	if s.RatePerMin < 0 {
		return true
	}
	s.limitOnce.Do(func() {
		per := s.RatePerMin
		if per == 0 {
			per = 60
		}
		s.limiter = newRateLimiter(per)
	})
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if key == "" {
		key = r.URL.Query().Get("token")
	}
	if key == "" {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			key = host
		} else {
			key = r.RemoteAddr
		}
	}
	return s.limiter.Allow(key)
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
