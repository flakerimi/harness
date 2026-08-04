package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newAuthedRecorder(srv *Server, path, token string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestRateLimiterPerKey(t *testing.T) {
	now := time.Now()
	l := newRateLimiter(60)
	l.now = func() time.Time { return now }

	for i := 0; i < 60; i++ {
		if !l.Allow("a") {
			t.Fatalf("call %d for key a should pass", i+1)
		}
	}
	if l.Allow("a") {
		t.Fatal("61st call for key a should be limited")
	}
	if !l.Allow("b") {
		t.Fatal("key b must not share key a's bucket")
	}

	// A second later one token has refilled.
	now = now.Add(time.Second)
	if !l.Allow("a") {
		t.Fatal("refill after 1s should allow one more")
	}
	if l.Allow("a") {
		t.Fatal("only one token refilled")
	}
}

func TestRateLimitedRequestGets429(t *testing.T) {
	srv := newTestServer(t)
	srv.Token = "secret"
	srv.RatePerMin = 2

	req := func() int {
		rec := newAuthedRecorder(srv, "/v1/profiles", "secret")
		return rec.Code
	}
	if req() != 200 || req() != 200 {
		t.Fatal("first two requests should pass")
	}
	if got := req(); got != 429 {
		t.Fatalf("third request = %d, want 429", got)
	}
}
