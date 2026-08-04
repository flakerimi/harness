package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flakerimi/harness/auth"
)

// fakeTokenEndpoint points the auth package's exchange at a local server for
// the duration of a test.
func fakeTokenEndpoint(t *testing.T) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "acc", "refresh_token": "ref", "expires_in": 3600,
		})
	}))
	t.Cleanup(ts.Close)
	old := auth.TokenURLForTest(ts.URL)
	t.Cleanup(func() { auth.TokenURLForTest(old) })
}

func withAuthStore(t *testing.T, srv *Server) *auth.Store {
	t.Helper()
	store := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	srv.AuthStore = func(string) *auth.Store { return store }
	return store
}

func startClaudeAuth(t *testing.T, srv *Server) string {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/claude/start", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	var out struct{ URL, State string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.URL, "claude.ai/oauth/authorize") || out.State == "" {
		t.Fatalf("start payload = %+v", out)
	}
	return out.State
}

func TestClaudeAuthFlow(t *testing.T) {
	srv := newTestServer(t)
	store := withAuthStore(t, srv)
	fakeTokenEndpoint(t)

	state := startClaudeAuth(t, srv)

	// Wrong state → 400.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/claude/complete",
		strings.NewReader(`{"code":"c#wrongstate"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong state = %d", rec.Code)
	}

	// Right state → credentials land in the store.
	rec = httptest.NewRecorder()
	body := `{"code":"thecode#` + state + `"}`
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/claude/complete", strings.NewReader(body)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "connected") {
		t.Fatalf("complete = %d %s", rec.Code, rec.Body.String())
	}
	creds, err := store.Load("claude")
	if err != nil || creds.Access != "acc" || creds.Refresh != "ref" {
		t.Fatalf("stored creds = %+v err=%v", creds, err)
	}

	// State is single use.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/claude/complete", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("replayed state = %d, want 400", rec.Code)
	}
}

func TestClaudeAuthUnconfigured(t *testing.T) {
	srv := newTestServer(t) // no AuthStore
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/claude/start", strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("unconfigured start = %d, want 501", rec.Code)
	}
}
