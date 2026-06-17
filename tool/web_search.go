package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// WebSearch returns web results for a query via DuckDuckGo's HTML endpoint —
// keyless and public. Override the engine with HARNESS_SEARCH_URL; any endpoint
// that accepts POST q=<query> and renders DuckDuckGo-style result anchors works
// (that's how Construct pointed it at Bing). Ported from Construct's web_search.
type WebSearch struct {
	HTTP *http.Client
}

func (WebSearch) Spec() Spec {
	return Spec{
		Name:        "web_search",
		Description: "Search the web. Returns up to 10 results (title, URL, snippet). Use for current information not in training data; follow up with web_fetch on promising URLs.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Search query."},
				"limit": map[string]any{"type": "integer", "description": "Max results (default 10, cap 20)."},
			},
			"required": []string{"query"},
		},
	}
}

func (w WebSearch) Run(ctx context.Context, input json.RawMessage, _ *Env) (Result, error) {
	var in struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(in.Query) == "" {
		return Result{Content: "query is required", IsError: true}, nil
	}
	if in.Limit <= 0 || in.Limit > 20 {
		in.Limit = 10
	}

	client := w.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	endpoint := os.Getenv("HARNESS_SEARCH_URL")
	if endpoint == "" {
		endpoint = "https://html.duckduckgo.com/html/"
	}

	form := url.Values{"q": {in.Query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{Content: fmt.Sprintf("search HTTP %d", resp.StatusCode), IsError: true}, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))

	results := parseDDG(string(body), in.Limit)
	if len(results) == 0 {
		return Result{Content: "no results"}, nil
	}
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", r.Snippet)
		}
	}
	return Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}

type searchResult struct{ Title, URL, Snippet string }

// DuckDuckGo HTML layout: result anchors carry class result__a (title+href) and
// result__snippet (description).
var (
	reDDGTitle   = regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__a[^"]*"[^>]+href="([^"]+)"[^>]*>(.+?)</a>`)
	reDDGSnippet = regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__snippet[^"]*"[^>]*>(.+?)</a>`)
)

func parseDDG(body string, limit int) []searchResult {
	titles := reDDGTitle.FindAllStringSubmatch(body, -1)
	snippets := reDDGSnippet.FindAllStringSubmatch(body, -1)
	out := make([]searchResult, 0, len(titles))
	for i, m := range titles {
		if i >= limit {
			break
		}
		r := searchResult{
			URL:   decodeDDGRedirect(m[1]),
			Title: strings.TrimSpace(stripTags(m[2])),
		}
		if i < len(snippets) {
			r.Snippet = strings.TrimSpace(stripTags(snippets[i][1]))
		}
		out = append(out, r)
	}
	return out
}

// decodeDDGRedirect unwraps DuckDuckGo's /l/?uddg=<encoded-url> result links.
func decodeDDGRedirect(u string) string {
	idx := strings.Index(u, "uddg=")
	if idx < 0 {
		if strings.HasPrefix(u, "//") {
			return "https:" + u
		}
		return u
	}
	tail := u[idx+len("uddg="):]
	tail, _, _ = strings.Cut(tail, "&")
	if dec, err := url.QueryUnescape(tail); err == nil {
		return dec
	}
	return u
}

// stripTags reuses the HTML helpers from web_fetch.go (same package).
func stripTags(s string) string {
	return htmlUnescape(reTag.ReplaceAllString(s, ""))
}
