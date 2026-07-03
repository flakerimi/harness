package auth

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

func TestGoogleRemoteStartMintsBoundConsentLinks(t *testing.T) {
	g := &GoogleRemote{ClientID: "id", ClientSecret: "sec", RedirectURL: "https://host/oauth/google/callback"}
	store := NewStore(t.TempDir() + "/auth.json")

	u, err := g.Start(store, "telegram:42")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if q.Get("redirect_uri") != g.RedirectURL || q.Get("client_id") != "id" {
		t.Errorf("consent url = %s", u)
	}
	if q.Get("state") == "" || q.Get("code_challenge") == "" {
		t.Error("state and PKCE challenge are required")
	}
	if !strings.Contains(q.Get("scope"), "gmail") {
		t.Errorf("scopes = %q", q.Get("scope"))
	}

	// Distinct starts mint distinct states.
	u2, _ := g.Start(store, "telegram:42")
	if p2, _ := url.Parse(u2); p2.Query().Get("state") == q.Get("state") {
		t.Error("states must be unique per start")
	}

	// Unknown state can't finish; a known one is single-use (consumed even on
	// exchange failure, so a link can't be replayed).
	if _, err := g.Finish(context.Background(), "nope", "code"); err == nil {
		t.Error("unknown state must fail")
	}

	// Unconfigured broker refuses to start.
	empty := &GoogleRemote{}
	if _, err := empty.Start(store, "x"); err == nil {
		t.Error("missing client config must fail Start")
	}
}
