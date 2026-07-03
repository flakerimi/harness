package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// GoogleRemote is the browser-elsewhere OAuth flow: the harness runs on a
// server, the user is on a phone. Start mints a consent URL whose state is
// bound to a pending entry (credential store + PKCE verifier); Finish is
// called by the public callback route with Google's code, exchanges it, and
// saves the credentials where the pending entry points. Requires a Google
// "Web application" OAuth client with the public callback registered as an
// authorized redirect URI.
type GoogleRemote struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string // public callback, e.g. https://host/oauth/google/callback

	mu      sync.Mutex
	pending map[string]remotePending
}

type remotePending struct {
	store    *Store
	verifier string
	meta     string
	created  time.Time
}

// remotePendingTTL bounds how long a consent link stays valid.
const remotePendingTTL = 15 * time.Minute

// Start registers a pending connect for the given credential store and returns
// the consent URL to hand to the user. meta rides along and is returned by
// Finish — e.g. a chat id, so the caller can confirm where the request began.
func (g *GoogleRemote) Start(store *Store, meta string) (string, error) {
	if g.ClientID == "" || g.ClientSecret == "" {
		return "", fmt.Errorf("google: client id/secret missing")
	}
	if g.RedirectURL == "" {
		return "", fmt.Errorf("google: no public redirect URL configured")
	}
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return "", err
	}
	sb := make([]byte, 16)
	if _, err := rand.Read(sb); err != nil {
		return "", err
	}
	state := hex.EncodeToString(sb)

	g.mu.Lock()
	if g.pending == nil {
		g.pending = map[string]remotePending{}
	}
	for s, p := range g.pending { // expire stale links
		if time.Since(p.created) > remotePendingTTL {
			delete(g.pending, s)
		}
	}
	g.pending[state] = remotePending{store: store, verifier: verifier, meta: meta, created: time.Now()}
	g.mu.Unlock()

	q := url.Values{}
	q.Set("client_id", g.ClientID)
	q.Set("redirect_uri", g.RedirectURL)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(GoogleDefaultScopes, " "))
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("include_granted_scopes", "true")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	return googleAuthURL + "?" + q.Encode(), nil
}

// Finish exchanges the callback's code for credentials and saves them into the
// pending entry's store. Returns the meta passed to Start. A state that is
// unknown or expired errors — a consent link can be used exactly once.
func (g *GoogleRemote) Finish(ctx context.Context, state, code string) (string, error) {
	g.mu.Lock()
	p, ok := g.pending[state]
	delete(g.pending, state)
	g.mu.Unlock()
	if !ok || time.Since(p.created) > remotePendingTTL {
		return "", fmt.Errorf("google: unknown or expired connect link — start again")
	}

	creds, err := googleTokenRequest(ctx, url.Values{
		"code":          {code},
		"client_id":     {g.ClientID},
		"client_secret": {g.ClientSecret},
		"redirect_uri":  {g.RedirectURL},
		"grant_type":    {"authorization_code"},
		"code_verifier": {p.verifier},
	}, "")
	if err != nil {
		return "", err
	}
	if err := p.store.Save("google", creds); err != nil {
		return "", fmt.Errorf("save credentials: %w", err)
	}
	return p.meta, nil
}
