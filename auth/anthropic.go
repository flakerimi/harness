package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// anthropicTokenURL is a var (not const) purely so tests can point the code
// exchange at an httptest server — go test must never touch the network.
var anthropicTokenURL = "https://platform.claude.com/v1/oauth/token"

// TokenURLForTest swaps the Anthropic token endpoint and returns the previous
// value. It exists only for tests in other packages (the server's OAuth
// endpoint tests) that must never touch the network.
func TokenURLForTest(u string) string {
	old := anthropicTokenURL
	anthropicTokenURL = u
	return old
}

// Public OAuth client identifier for the Claude Pro/Max login flow.
const anthropicClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

// AnthropicTokenSource yields short-lived Claude OAuth access tokens, refreshing
// from a long-lived refresh token and persisting the (rotated) credentials back
// to the Store. It satisfies provider.TokenSource structurally — no import of
// the provider package, so there's no dependency cycle.
type AnthropicTokenSource struct {
	store    *Store
	provider string // key in the auth file, e.g. "claude"
	http     *http.Client

	mu      sync.Mutex
	access  string
	expires int64 // unix ms
}

// NewAnthropicTokenSource builds a token source over a credential Store.
func NewAnthropicTokenSource(store *Store, providerKey string) *AnthropicTokenSource {
	return &AnthropicTokenSource{
		store:    store,
		provider: providerKey,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

// Token returns a valid access token, refreshing when the cached one is missing
// or within 60s of expiry. Anthropic rotates the refresh token on every refresh,
// so the new credentials are persisted before they're cached.
func (s *AnthropicTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	nowMs := time.Now().UnixMilli()
	if s.access != "" && s.expires-nowMs > 60_000 {
		return s.access, nil
	}

	creds, err := s.store.Load(s.provider)
	if err != nil {
		return "", err
	}
	// Reuse a still-valid on-disk access token without spending the refresh.
	if creds.Access != "" && creds.Expires-nowMs > 60_000 {
		s.access, s.expires = creds.Access, creds.Expires
		return s.access, nil
	}
	if creds.Refresh == "" {
		return "", fmt.Errorf("auth: no refresh token for %q", s.provider)
	}

	rotated, err := s.doRefresh(ctx, creds.Refresh)
	if err != nil {
		return "", err
	}
	if err := s.store.Save(s.provider, rotated); err != nil {
		return "", fmt.Errorf("auth: persist rotated token: %w", err)
	}
	s.access, s.expires = rotated.Access, rotated.Expires
	return s.access, nil
}

func (s *AnthropicTokenSource) doRefresh(ctx context.Context, refreshToken string) (*Credentials, error) {
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     anthropicClientID,
		"refresh_token": refreshToken,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicTokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: refresh: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: refresh HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("auth: refresh parse: %w", err)
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("auth: refresh returned empty access token")
	}

	newRefresh := refreshToken
	if out.RefreshToken != "" {
		newRefresh = out.RefreshToken
	}
	return &Credentials{
		Access:  out.AccessToken,
		Refresh: newRefresh,
		Expires: time.Now().UnixMilli() + out.ExpiresIn*1000,
	}, nil
}
