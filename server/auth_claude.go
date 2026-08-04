package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/flakerimi/harness/auth"
)

// pendingAuth is one in-flight paste-code login: the PKCE verifier waiting for
// its pasted code. Single use, short lived.
type pendingAuth struct {
	verifier string
	expires  time.Time
}

// claudeAuth guards the two-step paste-code flow for connecting a Claude
// subscription from a client app — the deployed daemon can't run the CLI's
// localhost callback, so the user pastes the code Anthropic's page displays.
type claudeAuth struct {
	mu      sync.Mutex
	pending map[string]pendingAuth
	now     func() time.Time
}

func (c *claudeAuth) put(state, verifier string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.now()
	for k, p := range c.pending { // sweep expired starts
		if t.After(p.expires) {
			delete(c.pending, k)
		}
	}
	c.pending[state] = pendingAuth{verifier: verifier, expires: t.Add(10 * time.Minute)}
}

func (c *claudeAuth) take(state string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pending[state]
	if !ok || c.now().After(p.expires) {
		return "", false
	}
	delete(c.pending, state) // single use
	return p.verifier, true
}

func (s *Server) claude() *claudeAuth {
	s.claudeOnce.Do(func() {
		s.claudeFlow = &claudeAuth{pending: map[string]pendingAuth{}, now: time.Now}
	})
	return s.claudeFlow
}

// handleClaudeAuthStart mints the authorize URL for the app to open in the
// browser; the PKCE verifier stays server-side, keyed by state.
func (s *Server) handleClaudeAuthStart(w http.ResponseWriter, r *http.Request) {
	if s.AuthStore == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "auth not configured"})
		return
	}
	url, verifier, state, err := auth.AnthropicManualAuthURL()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// The verifier never leaves this process; the browser only sees state.
	s.claude().put(state, verifier)
	writeJSON(w, http.StatusOK, map[string]string{"url": url, "state": state})
}

// handleClaudeAuthComplete exchanges the pasted code and persists credentials
// in the profile's auth store; the claude provider picks them up on next build.
func (s *Server) handleClaudeAuthComplete(w http.ResponseWriter, r *http.Request) {
	if s.AuthStore == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "auth not configured"})
		return
	}
	var req struct{ Code, State, Profile string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code required"})
		return
	}
	// A pasted "code#state" carries its own state; an explicit field wins.
	if i := strings.IndexByte(req.Code, '#'); i >= 0 && req.State == "" {
		req.State = req.Code[i+1:]
	}
	if req.State == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "state required — paste the full code Claude shows (code#state)"})
		return
	}
	verifier, ok := s.claude().take(req.State)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "login expired — start over"})
		return
	}
	creds, err := auth.ExchangeManualCode(r.Context(), req.Code, verifier, req.State)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	prof := orDefault(req.Profile, s.DefaultProfile)
	if err := s.AuthStore(prof).Save("claude", creds); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"connected": true})
}
