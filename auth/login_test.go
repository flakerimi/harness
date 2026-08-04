package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestManualAuthURLUsesHostedCallback(t *testing.T) {
	authURL, verifier, state, err := AnthropicManualAuthURL()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if got := q.Get("redirect_uri"); got != manualRedirectURI {
		t.Errorf("redirect_uri = %q, want hosted code page", got)
	}
	if q.Get("code") != "true" || q.Get("code_challenge_method") != "S256" {
		t.Errorf("missing manual-code params: %v", q)
	}
	if q.Get("state") != state {
		t.Error("URL state must match the returned state")
	}
	if state == verifier {
		t.Error("state must NOT be the PKCE verifier — it travels through the browser")
	}
}

func TestExchangeManualCodeSplitsFragment(t *testing.T) {
	var got map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "a", "refresh_token": "r", "expires_in": 3600,
		})
	}))
	defer ts.Close()
	old := anthropicTokenURL
	anthropicTokenURL = ts.URL
	defer func() { anthropicTokenURL = old }()

	creds, err := ExchangeManualCode(context.Background(), "thecode#thestate", "verif", "st-independent")
	if err != nil {
		t.Fatal(err)
	}
	if got["code"] != "thecode" {
		t.Errorf("posted code = %q, fragment must be stripped", got["code"])
	}
	if got["state"] != "st-independent" || got["code_verifier"] != "verif" {
		t.Errorf("state/code_verifier = %q/%q", got["state"], got["code_verifier"])
	}
	if got["redirect_uri"] != manualRedirectURI {
		t.Errorf("redirect_uri = %q, want hosted code page", got["redirect_uri"])
	}
	if creds.Access != "a" || creds.Refresh != "r" {
		t.Errorf("creds = %+v", creds)
	}
}
