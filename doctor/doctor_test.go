package doctor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// isolate retargets every outbound probe at base and clears ambient credentials.
// Without it a stray TELEGRAM_BOT_TOKEN or *_API_KEY in the developer's shell
// would make these tests hit the real network.
func isolate(t *testing.T, base string) {
	t.Helper()
	oldAPI, oldBases := telegramAPI, providerBases
	telegramAPI = base
	providerBases = map[string]string{}
	t.Cleanup(func() { telegramAPI, providerBases = oldAPI, oldBases })
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
}

func byName(checks []Check) map[string]Check {
	m := make(map[string]Check, len(checks))
	for _, c := range checks {
		m[c.Name] = c
	}
	return m
}

func TestSummaryMarksEachCheckAndTrimsTrailingNewline(t *testing.T) {
	out := Summary([]Check{
		{Name: "egress", OK: true, Detail: "HTTP 200", Millis: 12},
		{Name: "provider x", OK: false, Detail: "connection refused", Millis: 3},
	})
	if strings.HasSuffix(out, "\n") {
		t.Errorf("trailing newline not trimmed: %q", out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "✓ ") {
		t.Errorf("passing check should be marked ✓: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "✗ ") {
		t.Errorf("failing check should be marked ✗: %q", lines[1])
	}
	if !strings.Contains(lines[0], "egress") || !strings.Contains(lines[0], "12ms") || !strings.Contains(lines[0], "HTTP 200") {
		t.Errorf("line lost name/millis/detail: %q", lines[0])
	}
}

func TestSummaryOfNoChecksIsEmpty(t *testing.T) {
	if out := Summary(nil); out != "" {
		t.Errorf("want empty string, got %q", out)
	}
}

func TestHealthyRequiresEveryCheckToPass(t *testing.T) {
	if !Healthy(nil) {
		t.Error("no checks should count as healthy")
	}
	if !Healthy([]Check{{OK: true}, {OK: true}}) {
		t.Error("all-passing should be healthy")
	}
	if Healthy([]Check{{OK: true}, {OK: false}}) {
		t.Error("one failure should make it unhealthy")
	}
}

func TestTrimErrKeepsTheLastClause(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a: b", "b"},
		{"no colon here", "no colon here"},
		{`Get "http://x": dial tcp: connection refused`, "connection refused"},
		{"abc: ", "abc: "}, // ends with the separator: splitting would yield ""
	}
	for _, c := range cases {
		if got := trimErr(errors.New(c.in)); got != c.want {
			t.Errorf("trimErr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTrimErrTruncatesLongErrorsTo80(t *testing.T) {
	if got := trimErr(errors.New(strings.Repeat("x", 100))); len(got) != 80 {
		t.Errorf("want 80 chars, got %d", len(got))
	}
	got := trimErr(errors.New("ctx: " + strings.Repeat("y", 100)))
	if len(got) != 80 {
		t.Errorf("want 80 chars after splitting, got %d", len(got))
	}
	if strings.Contains(got, "ctx") {
		t.Errorf("prefix should have been dropped before truncating: %q", got)
	}
}

func TestProbeCountsAnyHTTPResponseAsWireUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := probe(context.Background(), "wire", srv.URL)
	if !c.OK {
		t.Fatalf("401 proves the wire is up, got %+v", c)
	}
	if c.Detail != "HTTP 401" {
		t.Errorf("detail = %q, want %q", c.Detail, "HTTP 401")
	}
}

func TestProbeExpectRequiresTheExactStatus(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()

	if c := probeExpect(context.Background(), "public url", srv.URL, http.StatusOK); !c.OK {
		t.Fatalf("matching status should pass, got %+v", c)
	}

	status = http.StatusInternalServerError
	c := probeExpect(context.Background(), "public url", srv.URL, http.StatusOK)
	if c.OK {
		t.Fatal("500 should fail a probe that expects 200")
	}
	if !strings.Contains(c.Detail, "HTTP 500") || !strings.Contains(c.Detail, "want 200") {
		t.Errorf("detail should name both statuses, got %q", c.Detail)
	}
}

func TestProbeReportsUnreachableHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listens on that port now

	c := probe(context.Background(), "dead", url)
	if c.OK {
		t.Fatal("probe of a closed port should fail")
	}
	if c.Detail == "" {
		t.Error("failure should carry a detail")
	}
}

func TestRunProbesOnlyProvidersWithAKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	isolate(t, srv.URL)
	providerBases = map[string]string{"CONFIGURED": srv.URL, "UNCONFIGURED": srv.URL}
	t.Setenv("CONFIGURED_API_KEY", "key")
	t.Setenv("UNCONFIGURED_API_KEY", "")

	got := byName(Run(context.Background(), "", ""))
	if _, ok := got["internet/telegram api"]; !ok {
		t.Error("egress check should always run")
	}
	if _, ok := got["provider configured"]; !ok {
		t.Error("provider with a key should be probed")
	}
	if _, ok := got["provider unconfigured"]; ok {
		t.Error("provider without a key should be skipped")
	}
	if _, ok := got["telegram bot token"]; ok {
		t.Error("empty bot token should be skipped")
	}
}

func TestRunProbesTokenSearxngAndPublicHealthz(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	isolate(t, srv.URL)
	t.Setenv("TELEGRAM_BOT_TOKEN", "tok")

	checks := Run(context.Background(), srv.URL, srv.URL)
	if !Healthy(checks) {
		t.Fatalf("all probes answered 200, want healthy: %s", Summary(checks))
	}
	got := byName(checks)
	for _, name := range []string{"internet/telegram api", "telegram bot token", "web search (searxng)", "public url"} {
		if _, ok := got[name]; !ok {
			t.Errorf("missing check %q", name)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(paths, " ")
	if !strings.Contains(joined, "/bottok/getMe") {
		t.Errorf("bot token should be verified via getMe, paths: %v", paths)
	}
	if !strings.Contains(joined, "/healthz") {
		t.Errorf("public url should be probed at /healthz, paths: %v", paths)
	}
}

func TestRunTrimsTrailingSlashFromPublicURL(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	isolate(t, srv.URL)
	Run(context.Background(), "", srv.URL+"/")

	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if p == "//healthz" {
			t.Fatalf("trailing slash not trimmed, requested %q", p)
		}
	}
}

func TestToolSpecIsReadOnly(t *testing.T) {
	spec := NewTool("", "").Spec()
	if spec.Name != "doctor" {
		t.Errorf("name = %q, want %q", spec.Name, "doctor")
	}
	if spec.Writes {
		t.Error("doctor only reads; Writes must stay false or the permission gate will prompt")
	}
	if spec.Description == "" {
		t.Error("tool needs a description for the model to route to it")
	}
}

func TestToolRunFlagsProblemsWhenAWireIsDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	isolate(t, srv.URL)

	res, err := NewTool("", srv.URL).Run(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Content, "PROBLEMS FOUND:\n") {
		t.Errorf("a failing check should be announced, got:\n%s", res.Content)
	}
}

func TestToolRunStaysQuietWhenEverythingPasses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	isolate(t, srv.URL)

	res, err := NewTool("", "").Run(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "PROBLEMS FOUND") {
		t.Errorf("healthy run should not announce problems, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "internet/telegram api") {
		t.Errorf("summary should still list the checks, got:\n%s", res.Content)
	}
}
