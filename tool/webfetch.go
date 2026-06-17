package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// WebFetch retrieves a URL and returns its text content (HTML reduced to plain
// text). It's the reference research tool — confined to http/https, size- and
// time-bounded, no credentials.
type WebFetch struct {
	HTTP *http.Client
}

func (WebFetch) Spec() Spec {
	return Spec{
		Name:        "web_fetch",
		Description: "Fetch a URL over http/https and return its text content (HTML is reduced to plain text).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The http(s) URL to fetch.",
				},
			},
			"required": []string{"url"},
		},
	}
}

const webFetchMaxBytes = 512 * 1024
const webFetchMaxText = 8000

func (w WebFetch) Run(ctx context.Context, input json.RawMessage, _ *Env) (Result, error) {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if !strings.HasPrefix(args.URL, "http://") && !strings.HasPrefix(args.URL, "https://") {
		return Result{Content: "url must be http(s)", IsError: true}, nil
	}

	client := w.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, args.URL, nil)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	req.Header.Set("User-Agent", "harness/0.1 (+web_fetch)")

	resp, err := client.Do(req)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{Content: fmt.Sprintf("HTTP %d for %s", resp.StatusCode, args.URL), IsError: true}, nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBytes))
	text := string(body)
	if strings.Contains(resp.Header.Get("Content-Type"), "html") || looksLikeHTML(text) {
		text = htmlToText(text)
	}
	text = strings.TrimSpace(text)
	if len(text) > webFetchMaxText {
		text = text[:webFetchMaxText] + "\n…(truncated)"
	}
	return Result{Content: fmt.Sprintf("%s\n\n%s", args.URL, text)}, nil
}

func looksLikeHTML(s string) bool {
	head := s
	if len(head) > 256 {
		head = head[:256]
	}
	head = strings.ToLower(head)
	return strings.Contains(head, "<html") || strings.Contains(head, "<!doctype html")
}

var (
	reScriptStyle = regexp.MustCompile(`(?is)<(script|style)\b[^>]*>.*?</\s*(script|style)\s*>`)
	reTag         = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpace       = regexp.MustCompile(`[ \t]+`)
	reBlankLines  = regexp.MustCompile(`\n\s*\n\s*`)
)

func htmlToText(s string) string {
	s = reScriptStyle.ReplaceAllString(s, " ")
	s = reTag.ReplaceAllString(s, " ")
	s = htmlUnescape(s)
	s = reSpace.ReplaceAllString(s, " ")
	s = reBlankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func htmlUnescape(s string) string {
	return strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`,
		"&#39;", "'", "&apos;", "'", "&nbsp;", " ",
	).Replace(s)
}
