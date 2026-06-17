package google

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flakerimi/harness/auth"
)

func storeWithGoogle(t *testing.T) *auth.Store {
	t.Helper()
	store := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err := store.Save("google", &auth.Credentials{
		Access:  "tok-access",
		Refresh: "r",
		Expires: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestCalendarListTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/calendar/v3/calendars/primary/events") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok-access" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{
			"id":"e1","summary":"TPRxCBRE | Meeting",
			"start":{"dateTime":"2026-06-12T16:00:00+02:00"},
			"end":{"dateTime":"2026-06-12T16:45:00+02:00"},
			"organizer":{"email":"orjeta.r@tpr.al","displayName":"Orjeta Ramaj"},
			"attendees":[{"email":"miro.sutton@cbre.com"},{"email":"timur.avadiev@cbre.com"}],
			"hangoutLink":"https://meet.google.com/ppc-bqey-fdx"
		}]}`))
	}))
	defer srv.Close()

	c := &Connector{
		store:   storeWithGoogle(t),
		tokens:  auth.NewGoogleTokenSource(storeWithGoogle(t), "id", "sec"),
		http:    http.DefaultClient,
		apiBase: srv.URL,
	}
	// Use one store consistently so the token source reads the same credential.
	c.tokens = auth.NewGoogleTokenSource(c.store, "id", "sec")

	in, _ := json.Marshal(map[string]any{"calendar_id": "primary"})
	res, err := (&calendarListTool{c: c}).Run(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	for _, want := range []string{"TPRxCBRE | Meeting", "miro.sutton@cbre.com", "timur.avadiev@cbre.com", "Orjeta Ramaj", "meet.google.com"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("result missing %q:\n%s", want, res.Content)
		}
	}
}

func TestStatusAndToolsGate(t *testing.T) {
	store := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	c := New(store, "id", "sec")

	if c.Status(context.Background()).Connected {
		t.Error("should not be connected before login")
	}
	if ts, _ := c.Tools(context.Background()); len(ts) != 0 {
		t.Error("no tools should be advertised before login")
	}

	if err := store.Save("google", &auth.Credentials{Access: "a", Refresh: "r", Expires: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if !c.Status(context.Background()).Connected {
		t.Error("should be connected after credentials saved")
	}
	if ts, _ := c.Tools(context.Background()); len(ts) != 2 {
		t.Errorf("want 2 calendar tools after login, got %d", len(ts))
	}
}
