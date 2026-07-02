package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func botTo(url, token string) *Bot {
	b := New(token)
	b.apiBase = url
	b.http = http.DefaultClient
	return b
}

func TestGetUpdatesAdvancesOffset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":[
			{"update_id":10,"message":{"message_id":1,"text":"hi","chat":{"id":42},"from":{"username":"flaker"}}},
			{"update_id":11,"message":{"message_id":2,"text":"yo","chat":{"id":42}}}]}`))
	}))
	defer srv.Close()

	b := botTo(srv.URL, "T")
	ups, err := b.GetUpdates(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 2 {
		t.Fatalf("want 2 updates, got %d", len(ups))
	}
	if ups[0].Message.sender() != "@flaker" {
		t.Errorf("sender = %q", ups[0].Message.sender())
	}
	if b.offset != 12 {
		t.Errorf("offset = %d, want 12 (max update_id + 1)", b.offset)
	}
}

func TestSendEncodesTokenAndPayload(t *testing.T) {
	var gotPath, gotChat, gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = r.ParseForm()
		gotChat = r.Form.Get("chat_id")
		gotText = r.Form.Get("text")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	b := botTo(srv.URL, "SECRET")
	if err := b.Send(context.Background(), 42, "hello world"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/botSECRET/sendMessage" {
		t.Errorf("path = %q", gotPath)
	}
	if gotChat != "42" || gotText != "hello world" {
		t.Errorf("payload chat=%q text=%q", gotChat, gotText)
	}

	// Empty reply is a no-op (no error).
	if err := b.Send(context.Background(), 42, "   "); err != nil {
		t.Errorf("empty send should be a no-op, got %v", err)
	}
}

func TestSetCommands(t *testing.T) {
	var gotPath, gotCommands string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = r.ParseForm()
		gotCommands = r.Form.Get("commands")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer srv.Close()

	b := botTo(srv.URL, "T")
	err := b.SetCommands(context.Background(), []Command{
		{Command: "model", Description: "switch model"},
		{Command: "help", Description: "help"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/botT/setMyCommands" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotCommands, `"command":"model"`) || !strings.Contains(gotCommands, `"command":"help"`) {
		t.Errorf("commands payload missing entries: %q", gotCommands)
	}
}

func TestRunDispatchesAndReplies(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	served := false
	sentOnce := make(chan struct{})
	var closeOnce sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			mu.Lock()
			first := !served
			served = true
			mu.Unlock()
			if first {
				_, _ = w.Write([]byte(`{"ok":true,"result":[
					{"update_id":1,"message":{"message_id":1,"text":"ping","chat":{"id":7},"from":{"first_name":"Ada"}}}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`)) // no more updates
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			_ = r.ParseForm()
			mu.Lock()
			sent = append(sent, r.Form.Get("text"))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
			closeOnce.Do(func() { close(sentOnce) })
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	b := botTo(srv.URL, "T")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	responder := func(_ context.Context, chatID int64, user, text string) string {
		return user + " said " + text + " in " + strconv.FormatInt(chatID, 10)
	}
	go func() { _ = b.Run(ctx, responder, nil, nil) }()

	<-sentOnce // a reply was delivered
	cancel()   // now stop the loop

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || !strings.Contains(sent[0], "Ada said ping in 7") {
		t.Errorf("unexpected sent messages: %#v", sent)
	}
}

func TestSplitMessage(t *testing.T) {
	// Short text: one chunk, untouched.
	if got := splitMessage("hello", 4000); len(got) != 1 || got[0] != "hello" {
		t.Errorf("short = %q", got)
	}

	// Long text splits, preferring line boundaries, nothing lost.
	var b strings.Builder
	for i := range 400 {
		fmt.Fprintf(&b, "line %03d: some meaningful content here\n", i)
	}
	chunks := splitMessage(b.String(), 4000)
	if len(chunks) < 2 {
		t.Fatalf("long text should split, got %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		if n := utf16Len(c); n > 4000 {
			t.Errorf("chunk %d is %d units", i, n)
		}
		if i < len(chunks)-1 && !strings.HasSuffix(c, "here") {
			t.Errorf("chunk %d should end at a line boundary: …%q", i, c[len(c)-20:])
		}
	}
	if joined := strings.Join(chunks, "\n"); !strings.Contains(joined, "line 000") || !strings.Contains(joined, "line 399") {
		t.Error("content lost in split")
	}

	// Emoji count as two units — a chunk of 3000 emoji must still fit.
	chunks = splitMessage(strings.Repeat("🚀", 3000), 4000)
	for i, c := range chunks {
		if n := utf16Len(c); n > 4000 {
			t.Errorf("emoji chunk %d is %d units", i, n)
		}
	}
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xFFFF {
			n++
		}
	}
	return n
}

func TestLongSendIsChunkedNeverDropped(t *testing.T) {
	var texts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		texts = append(texts, r.Form.Get("text"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	long := strings.Repeat("research finding paragraph.\n", 600) // ~16k chars — the bug that ate task deliveries
	if err := botTo(srv.URL, "T").Send(context.Background(), 7, long); err != nil {
		t.Fatal(err)
	}
	if len(texts) < 4 {
		t.Errorf("expected several sendMessage calls, got %d", len(texts))
	}
	for i, tx := range texts {
		if utf16Len(tx) > 4096 {
			t.Errorf("call %d over Telegram's limit: %d", i, utf16Len(tx))
		}
	}
}
