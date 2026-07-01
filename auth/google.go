package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token"
)

// GoogleDefaultScopes identify the user, grant read access to Calendar and
// Gmail, and allow composing Gmail drafts (compose creates drafts and can send;
// reading still needs gmail.readonly). Modeled on Construct's integration
// scopes. Note: an account connected before compose was added must reconnect
// (harness connect google) to grant it before drafting works.
var GoogleDefaultScopes = []string{
	"openid",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/calendar.readonly",
	"https://www.googleapis.com/auth/calendar.events.readonly",
	"https://www.googleapis.com/auth/gmail.readonly",
	"https://www.googleapis.com/auth/gmail.compose",
}

// GoogleLogin runs the Google OAuth flow (loopback redirect, PKCE, offline +
// consent so a refresh token is issued) and saves credentials under "google".
// Use a Google Cloud "Desktop app" OAuth client, which permits loopback
// redirects (http://localhost:53692/callback).
func GoogleLogin(ctx context.Context, store *Store, clientID, clientSecret string, scopes []string, onURL func(string)) (*Credentials, error) {
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("google: client id/secret missing (set google.client_id / client_secret in config, or GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET)")
	}
	if len(scopes) == 0 {
		scopes = GoogleDefaultScopes
	}

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, err
	}
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	server, err := startCallbackServer(verifier, codeCh, errCh)
	if err != nil {
		return nil, fmt.Errorf("start callback server: %w", err)
	}
	defer server.Close()

	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI())
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("include_granted_scopes", "true")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", verifier)
	authURL := googleAuthURL + "?" + q.Encode()
	if onURL != nil {
		onURL(authURL)
	}
	openBrowser(authURL)

	var code string
	select {
	case code = <-codeCh:
	case e := <-errCh:
		return nil, e
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	creds, err := googleTokenRequest(ctx, url.Values{
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI()},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	}, "")
	if err != nil {
		return nil, err
	}
	if err := store.Save("google", creds); err != nil {
		return nil, fmt.Errorf("save credentials: %w", err)
	}
	return creds, nil
}

// GoogleTokenSource yields Google access tokens, refreshing from the stored
// refresh token. Satisfies provider.TokenSource structurally.
type GoogleTokenSource struct {
	store        *Store
	clientID     string
	clientSecret string
	http         *http.Client

	mu      sync.Mutex
	access  string
	expires int64 // unix ms
}

// NewGoogleTokenSource builds a token source over the credential store.
func NewGoogleTokenSource(store *Store, clientID, clientSecret string) *GoogleTokenSource {
	return &GoogleTokenSource{
		store:        store,
		clientID:     clientID,
		clientSecret: clientSecret,
		http:         &http.Client{Timeout: 30 * time.Second},
	}
}

// Token returns a valid Google access token, refreshing when needed.
func (s *GoogleTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	if s.access != "" && s.expires-now > 60_000 {
		return s.access, nil
	}
	creds, err := s.store.Load("google")
	if err != nil {
		return "", err
	}
	if creds.Access != "" && creds.Expires-now > 60_000 {
		s.access, s.expires = creds.Access, creds.Expires
		return s.access, nil
	}
	if creds.Refresh == "" {
		return "", fmt.Errorf("google: no refresh token (run: harness login --provider google)")
	}

	rotated, err := googleTokenRequest(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {creds.Refresh},
		"client_id":     {s.clientID},
		"client_secret": {s.clientSecret},
	}, creds.Refresh)
	if err != nil {
		return "", err
	}
	if err := s.store.Save("google", rotated); err != nil {
		return "", fmt.Errorf("google: persist token: %w", err)
	}
	s.access, s.expires = rotated.Access, rotated.Expires
	return s.access, nil
}

// googleTokenRequest posts a form to Google's token endpoint. Google omits the
// refresh token on a refresh grant, so existingRefresh is carried forward.
func googleTokenRequest(ctx context.Context, form url.Values, existingRefresh string) (*Credentials, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("google token: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google token: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("google token parse: %w", err)
	}
	refresh := out.RefreshToken
	if refresh == "" {
		refresh = existingRefresh
	}
	return &Credentials{
		Access:  out.AccessToken,
		Refresh: refresh,
		Expires: time.Now().UnixMilli() + out.ExpiresIn*1000,
	}, nil
}
