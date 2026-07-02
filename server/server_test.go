package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/session"
	"github.com/flakerimi/harness/task"
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
		Factory: func(_ context.Context, _, _, _ string) (*agent.Agent, error) {
			return agent.New(textProvider{}, tool.NewRegistry(), agent.Options{}), nil
		},
		Sessions: func(_ string) *session.Store { return session.NewStore(dir) },
		Profiles: func() []ProfileInfo {
			return []ProfileInfo{{Name: "personal", Description: "you"}, {Name: "basecode", Description: "work"}}
		},
		DefaultProfile: "personal",
	}
}

func TestTokenAuth(t *testing.T) {
	srv := newTestServer(t)
	srv.Token = "secret"

	// No token → 401.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/profiles", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing token should be 401, got %d", rec.Code)
	}

	// Bearer header → ok.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/profiles", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "basecode") {
		t.Errorf("bearer auth = %d %q", rec.Code, rec.Body.String())
	}

	// ?token= query (for EventSource) → ok.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/profiles?token=secret", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("query-token auth = %d", rec.Code)
	}

	// Wrong token → 401; healthz stays open.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/profiles", nil)
	req.Header.Set("Authorization", "Bearer nope")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token should be 401, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("healthz must stay open, got %d", rec.Code)
	}
}

func TestCORSPreflight(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/v1/chat", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("OPTIONS preflight = %d, want 204", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("CORS origin header missing")
	}
}

func TestModelsAndProfilesEndpoints(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{"/v1/profiles", "/v1/models"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d", path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if !strings.Contains(rec.Body.String(), "fireworks") {
		t.Errorf("/v1/models should list providers: %s", rec.Body.String())
	}
}

func TestSessionHistoryEndpoint(t *testing.T) {
	srv := newTestServer(t)
	// Seed a session via chat.
	body := strings.NewReader(`{"profile":"personal","session":"h1","message":"hi"}`)
	srv.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat", body))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/h1?profile=personal", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"messages"`) {
		t.Errorf("session history = %d %q", rec.Code, rec.Body.String())
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

func TestTaskEndpoints(t *testing.T) {
	srv := newTestServer(t)
	srv.Tasks = task.NewStore(t.TempDir())
	h := srv.Handler()

	// Empty queue lists as [].
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/tasks", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty list = %d %q", rec.Code, rec.Body.String())
	}

	// Enqueue; empty profile falls back to the server default.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/tasks",
		strings.NewReader(`{"prompt":"research X","deliver":"telegram:1"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("enqueue = %d %q", rec.Code, rec.Body.String())
	}
	var created task.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Profile != "personal" || created.Status != task.Queued {
		t.Errorf("created = %+v", created)
	}

	// Fetch it back by id.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/tasks/"+created.ID, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "research X") {
		t.Errorf("show = %d %q", rec.Code, rec.Body.String())
	}

	// Bad enqueue → 400; unknown id → 404.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{"prompt":""}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty prompt = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/tasks/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing id = %d", rec.Code)
	}

	// Nil store → task routes absent; the request falls through to the index
	// catch-all rather than a task listing.
	srv2 := newTestServer(t)
	rec = httptest.NewRecorder()
	srv2.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/tasks", nil))
	if strings.Contains(rec.Body.String(), `"status"`) || !strings.Contains(rec.Body.String(), "service") {
		t.Errorf("nil store should not serve a task list, got %q", rec.Body.String())
	}
}
