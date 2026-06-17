package tool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebFetchStripsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><head><style>x{color:red}</style></head>" +
			"<body><h1>Hello</h1><p>World &amp; more</p><script>bad()</script></body></html>"))
	}))
	defer srv.Close()

	in, _ := json.Marshal(map[string]string{"url": srv.URL})
	res, err := WebFetch{}.Run(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "Hello") || !strings.Contains(res.Content, "World & more") {
		t.Errorf("expected stripped text content, got: %q", res.Content)
	}
	if strings.Contains(res.Content, "bad()") || strings.Contains(res.Content, "color:red") || strings.Contains(res.Content, "<h1>") {
		t.Errorf("script/style/tags not stripped: %q", res.Content)
	}
}

func TestWebFetchRejectsNonHTTP(t *testing.T) {
	in, _ := json.Marshal(map[string]string{"url": "file:///etc/passwd"})
	res, _ := WebFetch{}.Run(context.Background(), in, nil)
	if !res.IsError {
		t.Errorf("expected error for non-http url, got: %q", res.Content)
	}
}
