// Package doctor is the connectivity diagnostic: one call that checks every
// wire the assistant depends on — internet egress, the chat channel, each
// configured model provider, integrations — and says plainly what's broken.
// Exposed as `harness doctor` for humans and as a read-only tool so the
// assistant can diagnose itself when things feel off.
package doctor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Check is one probe's outcome.
type Check struct {
	Name   string
	OK     bool
	Detail string
	Millis int64
}

// providerBases maps provider slugs to a reachable endpoint; only providers
// with a configured key are probed. Any HTTP response (even 401) proves the
// wire — status codes are irrelevant here.
var providerBases = map[string]string{
	"ANTHROPIC":  "https://api.anthropic.com",
	"OPENAI":     "https://api.openai.com",
	"DEEPSEEK":   "https://api.deepseek.com",
	"MOONSHOT":   "https://api.moonshot.ai",
	"MISTRAL":    "https://api.mistral.ai",
	"OPENROUTER": "https://openrouter.ai",
	"FIREWORKS":  "https://api.fireworks.ai",
	"MIMO":       "https://api.xiaomimimo.com",
	"GEMINI":     "https://generativelanguage.googleapis.com",
	"TOGETHER":   "https://api.together.xyz",
	"XAI":        "https://api.x.ai",
	"ZAI":        "https://api.z.ai",
	"DASHSCOPE":  "https://dashscope-intl.aliyuncs.com",
}

// Run executes every applicable probe. searxngURL and publicURL are optional
// extras from config/env; empty skips them.
func Run(ctx context.Context, searxngURL, publicURL string) []Check {
	var checks []Check

	// Egress + chat channel in one: Telegram's API answers even without auth.
	checks = append(checks, probe(ctx, "internet/telegram api", "https://api.telegram.org"))

	// The bot token itself, when present.
	if tok := os.Getenv("TELEGRAM_BOT_TOKEN"); tok != "" {
		c := probeExpect(ctx, "telegram bot token", "https://api.telegram.org/bot"+tok+"/getMe", http.StatusOK)
		checks = append(checks, c)
	}

	// Each provider that has a key configured.
	for slug, base := range providerBases {
		if os.Getenv(slug+"_API_KEY") == "" {
			continue
		}
		checks = append(checks, probe(ctx, "provider "+strings.ToLower(slug), base))
	}

	if searxngURL != "" {
		checks = append(checks, probe(ctx, "web search (searxng)", searxngURL))
	}
	if publicURL != "" {
		checks = append(checks, probeExpect(ctx, "public url", strings.TrimRight(publicURL, "/")+"/healthz", http.StatusOK))
	}
	return checks
}

// probe counts any HTTP response as success — it tests the wire, not the auth.
func probe(ctx context.Context, name, url string) Check {
	return doProbe(ctx, name, url, 0)
}

// probeExpect additionally requires a specific status code.
func probeExpect(ctx context.Context, name, url string, want int) Check {
	return doProbe(ctx, name, url, want)
}

func doProbe(ctx context.Context, name, url string, want int) Check {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Check{Name: name, OK: false, Detail: err.Error()}
	}
	resp, err := (&http.Client{Timeout: 12 * time.Second}).Do(req)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		return Check{Name: name, OK: false, Detail: trimErr(err), Millis: ms}
	}
	defer resp.Body.Close()
	if want != 0 && resp.StatusCode != want {
		return Check{Name: name, OK: false, Detail: fmt.Sprintf("HTTP %d (want %d)", resp.StatusCode, want), Millis: ms}
	}
	return Check{Name: name, OK: true, Detail: fmt.Sprintf("HTTP %d", resp.StatusCode), Millis: ms}
}

// trimErr keeps error text short enough for a table row.
func trimErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i > 0 && i < len(s)-2 {
		s = s[i+2:]
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// Summary renders checks as aligned ✓/✗ lines.
func Summary(checks []Check) string {
	var b strings.Builder
	for _, c := range checks {
		mark := "✓"
		if !c.OK {
			mark = "✗"
		}
		fmt.Fprintf(&b, "%s %-26s %5dms  %s\n", mark, c.Name, c.Millis, c.Detail)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// Healthy reports whether every check passed.
func Healthy(checks []Check) bool {
	for _, c := range checks {
		if !c.OK {
			return false
		}
	}
	return true
}
