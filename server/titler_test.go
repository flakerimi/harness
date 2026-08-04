package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTitlerRunsOnceAndLandsInListing(t *testing.T) {
	srv := newTestServer(t)
	var calls atomic.Int32
	srv.Titler = func(_ context.Context, _, transcript string) (string, error) {
		calls.Add(1)
		if !strings.Contains(transcript, "User:") {
			t.Errorf("transcript not rendered: %q", transcript)
		}
		return `"Boiler repair chat"`, nil // quotes must be stripped
	}

	post := func(msg string) {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat",
			strings.NewReader(`{"session":"tt","message":"`+msg+`"}`)))
		if !strings.Contains(rec.Body.String(), "event: done") {
			t.Fatalf("turn did not finish: %s", rec.Body.String())
		}
	}
	post("my boiler leaks")
	post("still leaking")

	if got := calls.Load(); got != 1 {
		t.Fatalf("titler ran %d times, want 1", got)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions", nil))
	if !strings.Contains(rec.Body.String(), "Boiler repair chat") {
		t.Fatalf("listing missing generated title: %s", rec.Body.String())
	}
}
