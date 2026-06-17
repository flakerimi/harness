package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/session"
	"github.com/flakerimi/harness/tool"
)

// textProvider streams a fixed reply — a deterministic stand-in for a model.
type textProvider struct{}

func (textProvider) Name() string { return "text" }
func (textProvider) Stream(_ context.Context, _ provider.Request, emit func(provider.Event)) error {
	emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "hello "})
	emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "world"})
	emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	return nil
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	return &Server{
		Factory: func(_ context.Context, _ string) (*agent.Agent, error) {
			return agent.New(textProvider{}, tool.NewRegistry(), agent.Options{}), nil
		},
		Sessions:       func(_ string) *session.Store { return session.NewStore(dir) },
		DefaultProfile: "personal",
	}
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestChatStreamsAndPersists(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"profile":"personal","session":"s1","message":"hi"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", body)
	srv.Handler().ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	out := rec.Body.String()
	for _, want := range []string{"event: ready", "event: text", `"delta":"hello "`, "event: stop", "event: done"} {
		if !strings.Contains(out, want) {
			t.Errorf("SSE stream missing %q\n---\n%s", want, out)
		}
	}

	// The conversation should have persisted (user + assistant = 1 turn).
	metas, err := srv.Sessions("personal").List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != "s1" || metas[0].Turns != 1 {
		t.Errorf("session not persisted as expected: %+v", metas)
	}
}

func TestChatValidation(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"message":""}`))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty message should be 400, got %d", rec.Code)
	}
}

func TestSessionsEndpoint(t *testing.T) {
	srv := newTestServer(t)
	// Seed one session via a chat call.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"session":"abc","message":"hi"}`))
	srv.Handler().ServeHTTP(rec, req)

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/v1/sessions?profile=personal", nil)
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), `"abc"`) {
		t.Errorf("sessions endpoint = %d %q", rec2.Code, rec2.Body.String())
	}
}
