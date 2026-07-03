package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeliverWebhookPostsBothKeys(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
	}))
	defer srv.Close()

	if err := deliver(context.Background(), "webhook:"+srv.URL, "task done ✓"); err != nil {
		t.Fatal(err)
	}
	// Slack/Mattermost/Teams read "text"; Discord reads "content".
	if got["text"] != "task done ✓" || got["content"] != "task done ✓" {
		t.Errorf("payload = %v", got)
	}
}

func TestDeliverWebhookSurfacesHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no_service", http.StatusNotFound)
	}))
	defer srv.Close()

	err := deliver(context.Background(), "webhook:"+srv.URL, "hi")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v, want 404 surfaced", err)
	}
}

func TestDeliverRejectsBadTargets(t *testing.T) {
	ctx := context.Background()
	if err := deliver(ctx, "nonsense", "hi"); err == nil {
		t.Error("target without kind:dest should error")
	}
	if err := deliver(ctx, "smoke-signal:hill", "hi"); err == nil {
		t.Error("unknown kind should error")
	}
	// Empty text is a documented no-op, whatever the target.
	if err := deliver(ctx, "nonsense", "   "); err != nil {
		t.Errorf("empty text should be a no-op, got %v", err)
	}
}

func TestIsSilenceSwallowsSentinels(t *testing.T) {
	for _, s := range []string{"", "  ", "NOTHING", "nothing", "*Nothing*", "_NOTHING._", "`nothing`", "Nothing that needs you.", "*Nothing that needs you.*", "SILENCE"} {
		if !isSilence(s) {
			t.Errorf("%q should count as silence", s)
		}
	}
	for _, s := range []string{"Nothing urgent, but your 3pm moved to 4", "2 new startups overnight", "NOTHING beats this: you got the grant!"} {
		if isSilence(s) {
			t.Errorf("%q is a real message, must not be swallowed", s)
		}
	}
}
