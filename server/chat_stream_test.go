package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/tool"
)

// blockProvider stalls the turn until released — the deterministic stand-in
// for a long tool call while the client backgrounds or reconnects.
type blockProvider struct{ release chan struct{} }

func (b blockProvider) Name() string { return "block" }
func (b blockProvider) Stream(_ context.Context, _ provider.Request, emit func(provider.Event)) error {
	<-b.release
	emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "late"})
	emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	return nil
}

func withBlocking(srv *Server) blockProvider {
	bp := blockProvider{release: make(chan struct{})}
	srv.Factory = func(_ context.Context, _, _, _ string) (*agent.Agent, error) {
		return agent.New(bp, tool.NewRegistry(), agent.Options{}), nil
	}
	return bp
}

func waitRunning(t *testing.T, m *turnManager, key string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := m.Running(key); ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("turn never started")
}

func TestChatCarriesEventIDs(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"session":"ids","message":"hi"}`)
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat", body))
	out := rec.Body.String()
	if !strings.Contains(out, "id: 1\nevent: ready") {
		t.Errorf("first frame should be id 1 ready:\n%s", out)
	}
	if !strings.Contains(out, "id: 2\n") {
		t.Errorf("ids should increase:\n%s", out)
	}
}

func TestChatSurvivesClientDisconnect(t *testing.T) {
	srv := newTestServer(t)
	bp := withBlocking(srv)

	ctx, cancelReq := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"session":"s2","message":"go"}`)).WithContext(ctx)
	handlerDone := make(chan struct{})
	go func() {
		srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
		close(handlerDone)
	}()
	waitRunning(t, srv.turns(), "personal/s2")

	cancelReq() // the iOS app backgrounds: connection gone
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return on client disconnect")
	}

	close(bp.release) // turn finishes on its own
	waitIdle(t, srv.turns(), "personal/s2")

	// Late re-attach replays the whole turn including its done frame.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/chat/stream?session=s2&after=0", nil))
	out := rec.Body.String()
	for _, want := range []string{"event: ready", `"delta":"late"`, "event: stop", "event: done"} {
		if !strings.Contains(out, want) {
			t.Errorf("resumed stream missing %q\n---\n%s", want, out)
		}
	}

	// And the session persisted despite the vanished client.
	metas, err := srv.Sessions("personal").List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range metas {
		if m.ID == "s2" && m.Turns == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("session not persisted after disconnect: %+v", metas)
	}
}

func TestChatBusyReturns409(t *testing.T) {
	srv := newTestServer(t)
	bp := withBlocking(srv)

	go srv.Handler().ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"session":"s3","message":"one"}`)))
	waitRunning(t, srv.turns(), "personal/s3")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"session":"s3","message":"two"}`)))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "turn in progress") {
		t.Errorf("double start = %d %q, want 409", rec.Code, rec.Body.String())
	}

	close(bp.release)
	waitIdle(t, srv.turns(), "personal/s3")
}

func TestStreamResumeAfter(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"session":"s4","message":"hi"}`)))
	if !strings.Contains(rec.Body.String(), "event: done") {
		t.Fatalf("turn did not finish:\n%s", rec.Body.String())
	}

	// Resume after seq 3 skips ready + the first text delta.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/chat/stream?session=s4&after=3", nil))
	out := rec.Body.String()
	if strings.Contains(out, `"delta":"hello "`) {
		t.Errorf("resume replayed frames at or before the cursor:\n%s", out)
	}
	if !strings.Contains(out, "event: done") {
		t.Errorf("resume must end with the done frame:\n%s", out)
	}
}

// A session's SECOND turn must stream cleanly from its own first frame — no
// reset frame, no replay of the previous turn (the seq cursor keeps rising
// across journal resets, which once tricked Since(0) into reporting eviction).
func TestSecondPostHasCleanPrefix(t *testing.T) {
	srv := newTestServer(t)
	for _, msg := range []string{"first", "second"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat",
			strings.NewReader(`{"session":"sp","message":"`+msg+`"}`)))
		out := rec.Body.String()
		if strings.Contains(out, "event: reset") {
			t.Fatalf("POST %q carried a spurious reset:\n%s", msg, out)
		}
		if strings.Index(out, "event: ready") > strings.Index(out, "event: text") {
			t.Fatalf("POST %q replayed old frames before ready:\n%s", msg, out)
		}
		if !strings.Contains(out, "event: done") {
			t.Fatalf("POST %q missing done:\n%s", msg, out)
		}
	}
}
