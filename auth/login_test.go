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
	authURL, verifier, err := AnthropicManualAuthURL()
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
	if q.Get("state") != verifier {
		t.Error("state must carry the verifier")
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

	creds, err := ExchangeManualCode(context.Background(), "thecode#thestate", "verif")
	if err != nil {
		t.Fatal(err)
	}
	if got["code"] != "thecode" {
		t.Errorf("posted code = %q, fragment must be stripped", got["code"])
	}
	if got["state"] != "verif" || got["code_verifier"] != "verif" {
		t.Errorf("state/code_verifier = %q/%q, want the verifier", got["state"], got["code_verifier"])
	}
	if got["redirect_uri"] != manualRedirectURI {
		t.Errorf("redirect_uri = %q, want hosted code page", got["redirect_uri"])
	}
	if creds.Access != "a" || creds.Refresh != "r" {
		t.Errorf("creds = %+v", creds)
	}
}
