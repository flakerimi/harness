package google

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
	if ts, _ := c.Tools(context.Background()); len(ts) != 6 {
		t.Errorf("want 6 tools (calendar + gmail read/draft/send) after login, got %d", len(ts))
	}
}

func TestGmailDraftTool(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"draft-123","message":{"id":"m1","threadId":"t9"}}`))
	}))
	defer srv.Close()

	c := connWithServer(t, srv.URL)
	in, _ := json.Marshal(map[string]any{
		"to":          "orjeta.r@tpr.al",
		"subject":     "Re: TPRxCBRE | Meeting",
		"body":        "Thanks — 16:00 works for me.",
		"thread_id":   "t9",
		"in_reply_to": "<abc@mail.gmail.com>",
	})
	res, err := (&gmailDraftTool{c: c}).Run(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "draft-123") {
		t.Fatalf("draft result = %q err=%v", res.Content, res.IsError)
	}
	if gotPath != "/gmail/v1/users/me/drafts" || gotAuth != "Bearer tok-access" {
		t.Fatalf("unexpected request: path=%s auth=%s", gotPath, gotAuth)
	}

	// The posted payload must carry the thread id and a base64url raw message
	// that decodes to the expected headers and body.
	var payload struct {
		Message struct {
			Raw      string `json:"raw"`
			ThreadID string `json:"threadId"`
		} `json:"message"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("payload parse: %v", err)
	}
	if payload.Message.ThreadID != "t9" {
		t.Errorf("thread id not sent, got %q", payload.Message.ThreadID)
	}
	rawMsg, err := base64.URLEncoding.DecodeString(payload.Message.Raw)
	if err != nil {
		t.Fatalf("raw not base64url: %v", err)
	}
	for _, want := range []string{
		"To: orjeta.r@tpr.al",
		"Subject: Re: TPRxCBRE | Meeting",
		"In-Reply-To: <abc@mail.gmail.com>",
		"References: <abc@mail.gmail.com>",
		"Thanks — 16:00 works for me.",
	} {
		if !strings.Contains(string(rawMsg), want) {
			t.Errorf("raw message missing %q:\n%s", want, rawMsg)
		}
	}
}

func TestGmailSendTool(t *testing.T) {
	var gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m42","threadId":"t9","labelIds":["SENT"]}`))
	}))
	defer srv.Close()

	c := connWithServer(t, srv.URL)
	in, _ := json.Marshal(map[string]any{
		"to": "orjeta.r@tpr.al", "subject": "Re: Meeting", "body": "On my way.",
		"thread_id": "t9", "in_reply_to": "<abc@mail.gmail.com>",
	})
	res, err := (&gmailSendTool{c: c}).Run(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "sent email") || !strings.Contains(res.Content, "m42") {
		t.Fatalf("send result = %q err=%v", res.Content, res.IsError)
	}
	if gotPath != "/gmail/v1/users/me/messages/send" {
		t.Fatalf("wrong path: %s", gotPath)
	}
	// Message resource is posted directly (raw + threadId, not wrapped in "message").
	var payload struct {
		Raw      string `json:"raw"`
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("payload parse: %v", err)
	}
	if payload.ThreadID != "t9" || payload.Raw == "" {
		t.Errorf("payload wrong: %+v", payload)
	}
	raw, _ := base64.URLEncoding.DecodeString(payload.Raw)
	if !strings.Contains(string(raw), "To: orjeta.r@tpr.al") || !strings.Contains(string(raw), "In-Reply-To:") {
		t.Errorf("raw message missing headers:\n%s", raw)
	}
}

func TestGmailDraftValidation(t *testing.T) {
	c := connWithServer(t, "http://unused.invalid")
	in, _ := json.Marshal(map[string]any{"to": "", "subject": "s", "body": "b"})
	res, _ := (&gmailDraftTool{c: c}).Run(context.Background(), in, nil)
	if !res.IsError {
		t.Error("missing 'to' should be a validation error before any request")
	}
}

func connWithServer(t *testing.T, url string) *Connector {
	t.Helper()
	store := storeWithGoogle(t)
	return &Connector{
		store:   store,
		tokens:  auth.NewGoogleTokenSource(store, "id", "sec"),
		http:    http.DefaultClient,
		apiBase: url,
	}
}

func TestGmailListTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-access" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/gmail/v1/users/me/messages":
			if r.URL.Query().Get("q") != "is:unread" {
				t.Errorf("query not forwarded: %q", r.URL.Query().Get("q"))
			}
			_, _ = w.Write([]byte(`{"messages":[{"id":"m1"}]}`))
		case strings.HasSuffix(r.URL.Path, "/messages/m1"):
			if r.URL.Query().Get("format") != "metadata" {
				t.Errorf("expected metadata format, got %q", r.URL.Query().Get("format"))
			}
			_, _ = w.Write([]byte(`{"id":"m1","snippet":"Let's sync on the plan",
				"payload":{"headers":[
					{"name":"From","value":"Orjeta <orjeta.r@tpr.al>"},
					{"name":"Subject","value":"Quarterly plan"},
					{"name":"Date","value":"Mon, 16 Jun 2026 10:00:00 +0200"}]}}`))
		default:
			http.Error(w, "not found: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := connWithServer(t, srv.URL)
	in, _ := json.Marshal(map[string]any{"query": "is:unread"})
	res, err := (&gmailListTool{c: c}).Run(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	for _, want := range []string{"Quarterly plan", "orjeta.r@tpr.al", "Let's sync on the plan", "id: m1"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("list result missing %q:\n%s", want, res.Content)
		}
	}
}

func TestGmailGetTool(t *testing.T) {
	plain := base64.RawURLEncoding.EncodeToString([]byte("Hello — here is the full plan.\nRegards."))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages/m1") || r.URL.Query().Get("format") != "full" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"m1","snippet":"snip",
			"payload":{"mimeType":"multipart/alternative",
				"headers":[
					{"name":"From","value":"Boss <boss@x.com>"},
					{"name":"To","value":"me@x.com"},
					{"name":"Subject","value":"The plan"},
					{"name":"Date","value":"Mon, 16 Jun 2026 10:00:00 +0200"}],
				"parts":[
					{"mimeType":"text/plain","body":{"data":%q}},
					{"mimeType":"text/html","body":{"data":"PGI+aGk8L2I+"}}]}}`, plain)
	}))
	defer srv.Close()

	c := connWithServer(t, srv.URL)
	in, _ := json.Marshal(map[string]any{"message_id": "m1"})
	res, err := (&gmailGetTool{c: c}).Run(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	for _, want := range []string{"Subject: The plan", "Boss <boss@x.com>", "To: me@x.com", "here is the full plan", "Regards."} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("get result missing %q:\n%s", want, res.Content)
		}
	}
}

func TestGmailGetValidation(t *testing.T) {
	c := connWithServer(t, "http://unused")
	in, _ := json.Marshal(map[string]any{"message_id": ""})
	res, _ := (&gmailGetTool{c: c}).Run(context.Background(), in, nil)
	if !res.IsError {
		t.Error("empty message_id should be a validation error")
	}
}

func TestDecodeB64(t *testing.T) {
	raw := base64.RawURLEncoding.EncodeToString([]byte("hi there"))
	if got := decodeB64(raw); got != "hi there" {
		t.Errorf("decodeB64(raw url) = %q", got)
	}
	padded := base64.URLEncoding.EncodeToString([]byte("hi there"))
	if got := decodeB64(padded); got != "hi there" {
		t.Errorf("decodeB64(padded url) = %q", got)
	}
}
