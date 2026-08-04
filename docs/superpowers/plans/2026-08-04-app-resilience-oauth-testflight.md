# App Resilience, Hardening, Claude OAuth, Sessions, TestFlight — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turns survive app backgrounding with live re-attach; server hardened; Claude OAuth connectable from the iOS app; Claude-style session list; app shipped to TestFlight.

**Architecture:** A `TurnManager` in the server package detaches agent turns from HTTP request contexts and journals every SSE frame with a monotonic id; clients re-attach via `GET /v1/chat/stream?after=<seq>`. OAuth adds two token-guarded endpoints wrapping the existing PKCE helpers with a paste-code flow. The Flutter app gains lifecycle-aware resume, Keychain token storage, titled sessions, and a Connect-Claude screen.

**Tech Stack:** Go 1.2x (stdlib only — repo has no third-party deps in core), Flutter/Dart (`http`, `flutter_secure_storage`, `url_launcher`), `xcrun altool` for upload.

**Spec:** `docs/superpowers/specs/2026-08-04-app-resilience-oauth-testflight-design.md`

## Global Constraints

- Repos: engine `~/Harnes` (git, `flakerimi/harness`), app `~/dev/harness-app` (git, **no remote**), deploy `~/dev/donna` (git, `flakerimi/donna`).
- `go test ./...` must never need the network (INTEGRATIONS.md ground rule 5). All Go tests use fakes/httptest.
- Never commit or bake credentials: `auth.json`, `.env`, `*.p8` stay out of git and Docker images.
- No `Co-Authored-By` lines in any commit.
- Match existing code style: stdlib-only Go, table tests, comment density as in `server/server.go`.
- Config defaults: turn timeout 10 min, journal cap 512 events, max concurrent turns 4, rate limit 60 req/min per token+IP, keepalive ping 20 s.
- iOS: bundle `al.basecode.harnessApp`, team `9FHBCA6NT3`, Apple ID `dev@basecode.al`.

---

### Task 1: Land the pending OpenAI schema fix

The working tree at `~/Harnes` has an uncommitted change in `provider/openai.go` (adds empty `properties` to object tool schemas for strict validators like LM Studio). Validate it, test it, commit it — clears the dirty tree before real work starts.

**Files:**
- Modify: `provider/openai.go` (already modified — keep as-is)
- Test: `provider/openai_test.go` (add one test)
- Also commit untracked `fable.md` separately ONLY if the user confirms; otherwise leave it.

**Interfaces:**
- Consumes: `(o *OpenAI) buildBody(req Request) map[string]any` (existing, unexported — test from inside package).
- Produces: nothing new.

- [ ] **Step 1: Write the failing-if-reverted test** in `provider/openai_test.go`:

```go
func TestBuildBodyToolSchemaGainsProperties(t *testing.T) {
	o := NewOpenAI("openai", "http://unused", "k")
	req := Request{
		Model: "gpt-4o",
		Tools: []ToolSpec{{Name: "ping", Description: "d", Parameters: map[string]any{"type": "object"}}},
	}
	body := o.buildBody(req)
	tools := body["tools"].([]map[string]any)
	fn := tools[0]["function"].(map[string]any)
	params := fn["parameters"].(map[string]any)
	if _, ok := params["properties"]; !ok {
		t.Fatal("object schema without properties was not normalized")
	}
	// the original spec map must not be mutated
	if _, ok := req.Tools[0].Parameters["properties"]; ok {
		t.Fatal("tool's own Parameters map was mutated")
	}
}
```

Note: check the real field names first (`grep -n "type ToolSpec\|type Request" provider/provider.go`) and adjust the literal to match — the shape above follows the existing tests in `provider/stream_test.go`.

- [ ] **Step 2: Run it** — `cd ~/Harnes && go test ./provider/ -run TestBuildBodyToolSchemaGainsProperties -v` → PASS (the fix is already in the tree; the test guards it). Temporarily `git stash` and re-run to see it FAIL, then `git stash pop`.
- [ ] **Step 3: Full package check** — `go test ./provider/` → all PASS; `go vet ./provider/`.
- [ ] **Step 4: Commit** — `git add provider/openai.go provider/openai_test.go && git commit -m "provider/openai: normalize object tool schemas for strict validators (LM Studio)"`

---

### Task 2: Event journal (`server/journal.go`)

A per-turn append-only ring of SSE frames with monotonic seq, safe for one writer + N readers.

**Files:**
- Create: `server/journal.go`
- Test: `server/journal_test.go`

**Interfaces:**
- Produces:
  - `type frame struct { Seq uint64; Event string; Data []byte }`
  - `newJournal(cap int) *journal`
  - `(j *journal) Append(event string, data []byte) frame` — assigns next seq, evicts oldest beyond cap.
  - `(j *journal) Since(after uint64) (frames []frame, evicted bool)` — frames with `Seq > after`; `evicted` true when `after` predates the oldest retained frame (client must refetch history).
  - `(j *journal) Reset()` — clears frames, seq keeps rising monotonically across turns.

- [ ] **Step 1: Write failing tests** in `server/journal_test.go`:

```go
package server

import "testing"

func TestJournalAppendSince(t *testing.T) {
	j := newJournal(3)
	for _, e := range []string{"a", "b", "c"} {
		j.Append(e, []byte(`{}`))
	}
	got, evicted := j.Since(1)
	if evicted || len(got) != 2 || got[0].Event != "b" || got[1].Seq != 3 {
		t.Fatalf("Since(1) = %v evicted=%v", got, evicted)
	}
}

func TestJournalEviction(t *testing.T) {
	j := newJournal(2)
	for i := 0; i < 5; i++ {
		j.Append("e", []byte(`{}`))
	}
	if _, evicted := j.Since(1); !evicted {
		t.Fatal("Since before oldest retained frame must report evicted")
	}
	got, evicted := j.Since(3)
	if evicted || len(got) != 2 {
		t.Fatalf("Since(3) = %v evicted=%v", got, evicted)
	}
}

func TestJournalResetKeepsSeqMonotonic(t *testing.T) {
	j := newJournal(8)
	j.Append("a", nil)
	j.Reset()
	f := j.Append("b", nil)
	if f.Seq != 2 {
		t.Fatalf("seq after reset = %d, want 2", f.Seq)
	}
}
```

- [ ] **Step 2: Run** — `go test ./server/ -run TestJournal -v` → FAIL (undefined: newJournal).
- [ ] **Step 3: Implement** `server/journal.go`:

```go
package server

import "sync"

// frame is one journaled SSE event. Seq is monotonic per session (it survives
// journal resets), so a client's "resume after N" is unambiguous across turns.
type frame struct {
	Seq   uint64
	Event string
	Data  []byte
}

// journal is a bounded ring of frames: one writer (the turn goroutine), many
// readers (attached streams). Older frames are evicted past cap; readers that
// resume from before the window learn via evicted=true to refetch history.
type journal struct {
	mu     sync.Mutex
	frames []frame
	cap    int
	seq    uint64
}

func newJournal(capacity int) *journal { return &journal{cap: capacity} }

func (j *journal) Append(event string, data []byte) frame {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	f := frame{Seq: j.seq, Event: event, Data: data}
	j.frames = append(j.frames, f)
	if len(j.frames) > j.cap {
		j.frames = j.frames[len(j.frames)-j.cap:]
	}
	return f
}

func (j *journal) Since(after uint64) ([]frame, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.frames) > 0 && after < j.frames[0].Seq-1 {
		return append([]frame(nil), j.frames...), true
	}
	var out []frame
	for _, f := range j.frames {
		if f.Seq > after {
			out = append(out, f)
		}
	}
	return out, false
}

func (j *journal) Reset() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.frames = nil
}
```

- [ ] **Step 4: Run** — `go test ./server/ -run TestJournal -v` → PASS. `go test ./server/` → all PASS.
- [ ] **Step 5: Commit** — `git add server/journal.go server/journal_test.go && git commit -m "server: per-turn SSE event journal with monotonic seq and resume window"`

---

### Task 3: TurnManager (`server/turns.go`)

Runs turns detached from any HTTP connection; one running turn per session key; global concurrency cap; broadcasts journaled frames to subscribers.

**Files:**
- Create: `server/turns.go`
- Test: `server/turns_test.go`

**Interfaces:**
- Consumes: `journal` from Task 2; `agent.Handler` (`OnText/OnToolStart/OnToolResult/OnUsage/OnStop`).
- Produces:
  - `newTurnManager(maxConcurrent int, timeout time.Duration) *turnManager`
  - `(m *turnManager) Start(key string, run func(ctx context.Context, h agent.Handler)) error` — `errTurnBusy` if key already running, `errTurnCapacity` at the cap. `run` is executed on a fresh goroutine with `context.WithTimeout(context.Background(), timeout)`; the handler passed to it journals + broadcasts. Manager emits the final `done` frame itself? **No** — the caller's `run` sends `error`/`done` exactly as `handleChat` does today; the manager only marks the turn finished when `run` returns.
  - `(m *turnManager) Subscribe(key string, after uint64) (replay []frame, evicted bool, live <-chan frame, cancel func(), running bool)` — replay first, then read `live` until closed. `live` is nil when no turn is running (replay still returns the finished turn's tail).
  - `(m *turnManager) Running(key string) (latest uint64, ok bool)`

- [ ] **Step 1: Write failing tests** in `server/turns_test.go` (no real agent — `run` is a plain func):

```go
package server

import (
	"context"
	"testing"
	"time"

	"github.com/flakerimi/harness/agent"
)

func collect(live <-chan frame, into *[]frame, done chan<- struct{}) {
	for f := range live {
		*into = append(*into, f)
	}
	close(done)
}

func TestTurnSurvivesSubscriberLoss(t *testing.T) {
	m := newTurnManager(4, time.Minute)
	release := make(chan struct{})
	err := m.Start("p/s", func(ctx context.Context, h agent.Handler) {
		h.OnText("one")
		<-release
		h.OnText("two")
		h.OnStop("end")
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, live, cancel, running := m.Subscribe("p/s", 0)
	if !running {
		t.Fatal("expected running turn")
	}
	cancel() // client vanishes mid-turn
	_ = live
	close(release) // turn keeps going regardless

	waitIdle(t, m, "p/s")
	replay, evicted, live2, _, running2 := m.Subscribe("p/s", 1)
	if evicted || running2 || live2 != nil {
		t.Fatalf("finished turn: evicted=%v running=%v", evicted, running2)
	}
	// frames after seq 1: text "two" + stop
	if len(replay) != 2 || replay[0].Event != "text" || replay[1].Event != "stop" {
		t.Fatalf("replay = %+v", replay)
	}
}

func TestSecondStartIsBusy(t *testing.T) {
	m := newTurnManager(4, time.Minute)
	release := make(chan struct{})
	_ = m.Start("p/s", func(ctx context.Context, h agent.Handler) { <-release })
	if err := m.Start("p/s", func(ctx context.Context, h agent.Handler) {}); err != errTurnBusy {
		t.Fatalf("err = %v, want errTurnBusy", err)
	}
	close(release)
}

func TestConcurrencyCap(t *testing.T) {
	m := newTurnManager(1, time.Minute)
	release := make(chan struct{})
	_ = m.Start("p/a", func(ctx context.Context, h agent.Handler) { <-release })
	if err := m.Start("p/b", func(ctx context.Context, h agent.Handler) {}); err != errTurnCapacity {
		t.Fatalf("err = %v, want errTurnCapacity", err)
	}
	close(release)
}

// waitIdle polls until the key's turn has finished (test helper, 2s cap).
func waitIdle(t *testing.T, m *turnManager, key string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := m.Running(key); !ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("turn never finished")
}
```

- [ ] **Step 2: Run** — `go test ./server/ -run 'TestTurn|TestSecondStart|TestConcurrencyCap' -v` → FAIL (undefined: newTurnManager).
- [ ] **Step 3: Implement** `server/turns.go`:

```go
package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/tool"
)

var (
	errTurnBusy     = errors.New("turn in progress")
	errTurnCapacity = errors.New("server is at its concurrent-turn limit")
)

// turnState is one session's turn machinery. The journal outlives the turn so
// late resumers can still replay the tail; it resets when the next turn starts.
type turnState struct {
	journal *journal
	subs    map[chan frame]struct{}
	running bool
}

// turnManager runs agent turns detached from any HTTP connection. A client
// disconnect only removes a subscriber; the turn finishes on its own context.
type turnManager struct {
	mu      sync.Mutex
	turns   map[string]*turnState
	active  int
	max     int
	timeout time.Duration
	journalCap int
}

func newTurnManager(maxConcurrent int, timeout time.Duration) *turnManager {
	return &turnManager{
		turns: map[string]*turnState{}, max: maxConcurrent,
		timeout: timeout, journalCap: 512,
	}
}

func (m *turnManager) state(key string) *turnState {
	st, ok := m.turns[key]
	if !ok {
		st = &turnState{journal: newJournal(m.journalCap), subs: map[chan frame]struct{}{}}
		m.turns[key] = st
	}
	return st
}

func (m *turnManager) Start(key string, run func(ctx context.Context, h agent.Handler)) error {
	m.mu.Lock()
	st := m.state(key)
	if st.running {
		m.mu.Unlock()
		return errTurnBusy
	}
	if m.active >= m.max {
		m.mu.Unlock()
		return errTurnCapacity
	}
	st.running = true
	st.journal.Reset()
	m.active++
	m.mu.Unlock()

	h := &journalHandler{m: m, key: key}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
		defer cancel()
		defer func() {
			m.mu.Lock()
			st.running = false
			m.active--
			for ch := range st.subs {
				close(ch)
				delete(st.subs, ch)
			}
			m.mu.Unlock()
		}()
		run(ctx, h)
	}()
	return nil
}

func (m *turnManager) Subscribe(key string, after uint64) (replay []frame, evicted bool, live <-chan frame, cancel func(), running bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.state(key)
	replay, evicted = st.journal.Since(after)
	if !st.running {
		return replay, evicted, nil, func() {}, false
	}
	ch := make(chan frame, 64)
	st.subs[ch] = struct{}{}
	cancel = func() {
		m.mu.Lock()
		if _, ok := st.subs[ch]; ok {
			delete(st.subs, ch)
			close(ch)
		}
		m.mu.Unlock()
	}
	return replay, evicted, ch, cancel, true
}

func (m *turnManager) Running(key string) (uint64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.turns[key]
	if !ok || !st.running {
		return 0, false
	}
	st.journal.mu.Lock()
	seq := st.journal.seq
	st.journal.mu.Unlock()
	return seq, true
}

// emit journals a frame and fans it out; a slow subscriber's full buffer drops
// that subscriber's frame rather than blocking the turn (it can resume by seq).
func (m *turnManager) emit(key, event string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	m.mu.Lock()
	st := m.state(key)
	f := st.journal.Append(event, payload)
	for ch := range st.subs {
		select {
		case ch <- f:
		default:
		}
	}
	m.mu.Unlock()
}

// journalHandler adapts agent.Handler onto the manager's emit.
type journalHandler struct {
	m   *turnManager
	key string
}

func (h *journalHandler) OnText(delta string) {
	h.m.emit(h.key, "text", map[string]string{"delta": delta})
}
func (h *journalHandler) OnToolStart(name, id string) {
	h.m.emit(h.key, "tool_start", map[string]string{"name": name, "id": id})
}
func (h *journalHandler) OnToolResult(name string, res tool.Result) {
	h.m.emit(h.key, "tool_result", map[string]any{"name": name, "content": res.Content, "is_error": res.IsError})
}
func (h *journalHandler) OnUsage(u provider.Usage) { h.m.emit(h.key, "usage", u) }
func (h *journalHandler) OnStop(reason string) {
	h.m.emit(h.key, "stop", map[string]string{"reason": reason})
}
func (h *journalHandler) OnRoute(tier, model string) {
	h.m.emit(h.key, "route", map[string]string{"tier": tier, "model": model})
}

// send lets the chat handler push ready/error/done through the same journal.
func (h *journalHandler) send(event string, data any) { h.m.emit(h.key, event, data) }
```

- [ ] **Step 4: Run** — the three tests → PASS; `go test -race ./server/` → PASS.
- [ ] **Step 5: Commit** — `git add server/turns.go server/turns_test.go && git commit -m "server: turn manager — detached turns, journaled broadcast, busy/capacity guards"`

---

### Task 4: Wire `/v1/chat` to the manager + add `GET /v1/chat/stream`

**Files:**
- Modify: `server/server.go` — `handleChat` (line ~418), route table (~line 253), `Server` struct (add `Turns *turnManager` initialized lazily, plus `TurnTimeout time.Duration`, `MaxTurns int` config fields with the spec defaults).
- Test: `server/server_test.go` (extend)

**Interfaces:**
- Consumes: Task 3's `turnManager`.
- Produces (HTTP contract the app relies on):
  - `POST /v1/chat` — unchanged request body. SSE frames now carry `id: <seq>` before `event:`. Busy session → `409 {"error":"turn in progress","seq":<latest>}`.
  - `GET /v1/chat/stream?session=&profile=&after=<seq>` — SSE: replays `> after` then live; `evicted` replay is prefixed by frame `event: reset` `data: {}` telling the client to refetch history first. Ends when the turn ends (after `done`), or immediately after replay if no turn is running.
  - Keepalive `: ping` comment every 20 s while a live stream is idle.

- [ ] **Step 1: Write failing tests** (extend `server/server_test.go`; follow its existing fixture style — it already builds a `Server` with a mock factory):

```go
// TestChatCarriesEventIDs: POST /v1/chat with the mock provider; assert the
// response body contains "id: 1" before the first "event:" line and that ids
// increase.

// TestChatSurvivesClientDisconnect: start a turn whose mock provider blocks
// until released; cancel the POST request's context (httptest + context);
// release; then GET /v1/chat/stream?after=0 and assert the full frame
// sequence including the final done frame arrives.

// TestChatBusyReturns409: while a turn blocks, second POST → 409 and body
// contains "turn in progress".

// TestStreamResumeAfter: after a finished turn, GET /v1/chat/stream?after=N
// returns only frames > N.
```

Write these as real Go — use the existing test helpers in `server_test.go` (look at `TestChatStreamsSSE` or nearest equivalent for the factory/mock wiring pattern; `provider.NewMock()` drives deterministic turns). The mock-blocking trick: the existing tests show how the factory injects behavior — if the mock provider can't block, inject a blocking `tool` or wrap `Factory` to return an agent whose provider is a locally-defined `provider.Provider` stub that waits on a channel.

- [ ] **Step 2: Run** — new tests FAIL.
- [ ] **Step 3: Rewrite `handleChat`** — everything before the `flusher` check stays; after computing `content`, `prof`, `sessID`:

```go
	key := prof + "/" + sessID

	store := s.Sessions(prof)
	sess, err := store.Load(sessID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if req.Provider != "" {
		sess.Provider = req.Provider
		sess.Model = req.Model
	} else if req.Model != "" {
		sess.Model = req.Model
	}

	// The agent is built on a background-derived context inside the manager;
	// r.Context() must not leak into the turn (that was the original bug).
	err = s.turns().Start(key, func(ctx context.Context, h agent.Handler) {
		jh := h.(*journalHandler)
		ag, ferr := s.Factory(ctx, prof, sess.Provider, sess.Model)
		if ferr != nil {
			jh.send("error", map[string]string{"error": ferr.Error()})
			jh.send("done", map[string]any{"turns": sess.Turns()})
			return
		}
		jh.send("ready", map[string]string{"profile": prof, "session": sess.ID, "provider": sess.Provider, "model": sess.Model})
		history, runErr := ag.ContinueWith(ctx, sess.History, content, h)
		sess.History = history
		if serr := store.Save(sess); serr != nil {
			jh.send("error", map[string]string{"error": "save: " + serr.Error()})
		}
		if runErr != nil {
			jh.send("error", map[string]string{"error": runErr.Error()})
		}
		s.titleSession(store, sess) // Task 7 adds this; until then omit the line
		jh.send("done", map[string]any{"turns": sess.Turns()})
	})
	switch err {
	case errTurnBusy:
		latest, _ := s.turns().Running(key)
		writeJSON(w, http.StatusConflict, map[string]any{"error": "turn in progress", "seq": latest})
		return
	case errTurnCapacity:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	s.streamFrom(w, r, flusher, key, 0)
```

Add the shared streamer + the new endpoint:

```go
// streamFrom subscribes to a session's turn and writes SSE until the turn
// ends or the client goes away. Client disconnect never cancels the turn.
func (s *Server) streamFrom(w http.ResponseWriter, r *http.Request, flusher http.Flusher, key string, after uint64) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	replay, evicted, live, cancel, _ := s.turns().Subscribe(key, after)
	defer cancel()
	writeFrame := func(f frame) {
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", f.Seq, f.Event, f.Data)
	}
	if evicted {
		fmt.Fprint(w, "event: reset\ndata: {}\n\n")
	}
	for _, f := range replay {
		writeFrame(f)
	}
	flusher.Flush()
	if live == nil {
		return
	}
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case f, ok := <-live:
			if !ok {
				return
			}
			writeFrame(f)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	prof := orDefault(r.URL.Query().Get("profile"), s.DefaultProfile)
	sessID := orDefault(r.URL.Query().Get("session"), "default")
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	s.streamFrom(w, r, flusher, prof+"/"+sessID, after)
}
```

Plus on `Server`: `turnsOnce sync.Once; turnMgr *turnManager` and

```go
func (s *Server) turns() *turnManager {
	s.turnsOnce.Do(func() {
		timeout := s.TurnTimeout
		if timeout == 0 {
			timeout = 10 * time.Minute
		}
		max := s.MaxTurns
		if max == 0 {
			max = 4
		}
		s.turnMgr = newTurnManager(max, timeout)
	})
	return s.turnMgr
}
```

Route: `mux.Handle("GET /v1/chat/stream", s.guard(s.handleChatStream))` and add it to the endpoints list at line ~323. The old `sseHandler` type stays for nothing — delete it and its methods (the journalHandler replaces it; `web/` UI consumes the same SSE with extra `id:` lines, which EventSource parses natively).

- [ ] **Step 4: Run** — `go test -race ./server/` → all PASS (old chat tests updated for `id:` lines as needed).
- [ ] **Step 5: Commit** — `git add -u server/ && git commit -m "server: detached turns with journaled resume — POST /v1/chat + GET /v1/chat/stream"`

---

### Task 5: Server hardening

**Files:**
- Modify: `server/server.go` (`authorized`, `Handler()` route wiring, body caps)
- Create: `server/ratelimit.go`
- Test: `server/ratelimit_test.go`, extend `server/server_test.go`

**Interfaces:**
- Produces: `newRateLimiter(perMin int) *rateLimiter`, `(l *rateLimiter) Allow(key string) bool` (token bucket, refill perMin/60 per second, burst = perMin). Wired inside `guard` only (streams established via guard count once at request start).

- [ ] **Step 1: Failing tests.** `TestRateLimiter`: 60/min limiter allows 60 immediate calls for key "a", 61st returns false, key "b" unaffected; after a simulated refill (export a test hook `refillAt time.Time` or inject a clock func `now func() time.Time`) it allows again. `TestGuardConstantTime`: `authorized` still accepts good token and rejects bad (behavioral — the constant-time property is by construction). `TestChatBodyCap`: POST /v1/chat with a >12 MB body → 413.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement.**
  - `server/ratelimit.go`: token bucket per key in a `map[string]*bucket` behind a mutex, `now func() time.Time` field defaulting to `time.Now` for testability; opportunistic eviction of idle buckets (>10 min unused) on each Allow.
  - In `guard`: rate-limit key = bearer token (or IP via `r.RemoteAddr` when token empty): `if !s.limiter().Allow(key) { writeJSON(w, 429, ...); return }`.
  - Token compare (find `authorized`, replace `==` with):

```go
	subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) == 1
```

  (`crypto/subtle` is already imported at server.go:16 — verify, else add.)
  - Body caps in `Handler()` wiring: wrap chat with `http.MaxBytesHandler(h, 12<<20)` and all other `/v1` POSTs with `1<<20`.
- [ ] **Step 4: Run** — `go test -race ./server/` → PASS.
- [ ] **Step 5: Daemon timeouts.** Find the `http.Server` construction (`grep -rn "http.Server{" ~/Harnes/cmd ~/Harnes/harness`); set `ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second` (NO WriteTimeout — it kills streams). Config knobs `server.max_turns`, `server.turn_timeout`, `server.rate_per_min` plumbed where the daemon builds `server.Server{...}` (follow how `Token` gets there — `grep -rn "server.Server{" ~/Harnes`).
- [ ] **Step 6: auth file perms.** In `auth/store.go` `Save`: ensure `os.WriteFile(path, b, 0o600)` and `os.Chmod(path, 0o600)` after (covers pre-existing files). Test: `TestStoreSavePerms` asserts `fi.Mode().Perm() == 0o600`. Verify `.gitignore`/`.dockerignore` in all three repos cover `auth.json` (add if missing — check first: `grep -l "auth.json" ~/Harnes/.gitignore ~/Harnes/.dockerignore ~/dev/donna/.gitignore ~/dev/donna/.dockerignore`).
- [ ] **Step 7: Commit** — `git add -A server/ auth/ && git commit -m "server: rate limiting, constant-time auth, body caps, listener timeouts; auth store 0600"`

---

### Task 6: Claude OAuth paste-code endpoints

**Files:**
- Modify: `auth/login.go` (manual-code URL variant + exported exchange), `auth/anthropic.go` (make `anthropicTokenURL` a `var` for tests)
- Create: `server/auth_claude.go`
- Test: `server/auth_claude_test.go`, `auth/login_test.go` (new)

**Interfaces:**
- Produces in `auth`:
  - `AnthropicManualAuthURL() (authURL, verifier string, err error)` — same as `AnthropicAuthURL` but `redirect_uri` set to the hosted code-display page (the same one Claude Code's paste mode uses: `https://platform.claude.com/oauth/code/callback`) so no localhost is involved.
  - `ExchangeManualCode(ctx context.Context, code, verifier string) (*Credentials, error)` — wraps the existing `exchangeCode`, first splitting a pasted `code#state` on `#` (Claude's code page shows `code#state`; the fragment is the state which must equal the verifier we minted).
- Produces in `server`:
  - `POST /v1/auth/claude/start` → `200 {"url": "...", "state": "..."}`; verifier stored in `map[state]pendingAuth{verifier, expires}` on the Server (mutex, 10-min TTL, swept on each start).
  - `POST /v1/auth/claude/complete {"code": "...", "state": "..."}` → exchanges, saves via `s.AuthStore(profile).Save("claude", creds)` → `200 {"connected": true}`. Missing/expired state → `400 {"error":"login expired — start over"}`.

- [ ] **Step 1: Failing tests.**
  - `auth/login_test.go`: `TestManualAuthURLUsesHostedCallback` (parse URL, assert `redirect_uri` query equals the hosted callback and `code=true` present); `TestExchangeManualCodeSplitsFragment` — point `anthropicTokenURL` at an `httptest.Server` asserting the POSTed JSON carries `code` without the fragment and `state` equal to the verifier, returning `{"access_token":"a","refresh_token":"r","expires_in":3600}`; assert saved fields.
  - `server/auth_claude_test.go`: start → returns url+state; complete with wrong state → 400; complete with the minted state (token URL faked as above) → 200 and `AuthStore("personal").Load("claude")` returns access "a"; state single-use (second complete → 400).
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement** per the interfaces above. `server/auth_claude.go` sketch:

```go
package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/flakerimi/harness/auth"
)

type pendingAuth struct {
	verifier string
	expires  time.Time
}

// claudeAuth guards the two-step paste-code flow. State keys are single-use
// and expire after 10 minutes; a fresh /start invalidates nothing else.
type claudeAuth struct {
	mu      sync.Mutex
	pending map[string]pendingAuth
}

func (s *Server) handleClaudeAuthStart(w http.ResponseWriter, r *http.Request) {
	if s.AuthStore == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "auth not configured"})
		return
	}
	url, verifier, err := auth.AnthropicManualAuthURL()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	state := verifier // PKCE verifier doubles as state, as in the CLI flow
	s.claude().put(state, verifier)
	writeJSON(w, http.StatusOK, map[string]string{"url": url, "state": state})
}

func (s *Server) handleClaudeAuthComplete(w http.ResponseWriter, r *http.Request) {
	var req struct{ Code, State, Profile string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code required"})
		return
	}
	// A pasted "code#state" carries its own state; explicit field wins.
	code := req.Code
	if i := strings.IndexByte(code, '#'); i >= 0 {
		if req.State == "" {
			req.State = code[i+1:]
		}
		code = code[:i]
	}
	verifier, ok := s.claude().take(req.State)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "login expired — start over"})
		return
	}
	creds, err := auth.ExchangeManualCode(r.Context(), code, verifier)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	prof := orDefault(req.Profile, s.DefaultProfile)
	if err := s.AuthStore(prof).Save("claude", creds); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"connected": true})
}
```

(`s.claude()` lazily builds the `claudeAuth` with `put`/`take` doing TTL sweep + single-use delete — same `sync.Once` pattern as `s.turns()`.) Routes (guarded): `POST /v1/auth/claude/start`, `POST /v1/auth/claude/complete`. Add claude to the `/v1/connectors` response: in the daemon's `Connectors` func (find with `grep -rn "ConnectorInfo{" ~/Harnes --include="*.go" | grep -v _test`), append a claude entry whose status derives from `AuthStore(profile).Load("claude")` (connected when present and `Expires` in future or refresh token exists).

- [ ] **Step 4: Run** — `go test ./auth/ ./server/` → PASS. **Manual sanity (network, outside go test):** `curl -s -X POST localhost:8080/v1/auth/claude/start -H "Authorization: Bearer $TOKEN"` returns a URL that opens to a Claude consent page. Do NOT complete it yet.
- [ ] **Step 5: Commit** — `git add auth/ server/ && git commit -m "server+auth: Claude OAuth paste-code flow — /v1/auth/claude/{start,complete}"`

---

### Task 7: Session titles + newest-first list

**Files:**
- Modify: `session/session.go` (add `Title` to `Session`; `List()` uses stored title, sorts by `Updated` desc), `server/server.go` (title hook + `/v1/sessions` handler exposes `title`/`updated`)
- Test: `session/session_test.go`, `server/server_test.go`

**Interfaces:**
- Produces:
  - `Session.Title string` (`json:"title,omitempty"`).
  - `Server.Titler func(ctx context.Context, profile, transcript string) (string, error)` — optional; daemon wires it to a one-shot completion on the session's provider (≤6 words prompt below). Nil → titles stay first-message snippets (existing behavior).
  - `(s *Server) titleSession(store *session.Store, sess *session.Session)` — called in the turn goroutine after save (Task 4 left the call site); no-op unless `Titler != nil && sess.Title == "" && sess.Turns() == 1`; runs `go func()` with 30 s timeout; on success sets `sess.Title` and re-saves. Errors are dropped (title stays empty; next turn won't retry since Turns()>1 — acceptable per spec).

- [ ] **Step 1: Failing tests.** `session`: `TestListPrefersStoredTitle` (save session with Title "Fix the boiler", `List()` meta uses it over the snippet), `TestListNewestFirst` (save two, second has later mtime, comes first). `server`: `TestSessionsEndpointCarriesTitle` (GET /v1/sessions includes `"title"` and `"updated"` fields, newest first); `TestTitlerRunsOnce` (fake Titler counts calls; two turns on one session → 1 call).
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement.** In `List()` (session.go:137-155): `title := sess.Title; if title == "" { title = clipText(firstUserText(sess.History), 60) }`; after building the slice add `sort.Slice(metas, func(i, j int) bool { return metas[i].Updated.After(metas[j].Updated) })`. Find the `/v1/sessions` handler (`grep -n "v1/sessions" server/server.go`) and make its JSON include `"title"` and `"updated"` (RFC3339). Titler prompt (in the daemon wiring, alongside the existing Factory construction):

```go
	// One-shot title: same provider stack as the session, cheapest model tier.
	srv.Titler = func(ctx context.Context, profile, transcript string) (string, error) {
		return runOneShot(ctx, profile, "Title this conversation in at most 6 plain words. Reply with the title only, no quotes.\n\n"+transcript)
	}
```

where `runOneShot` reuses however the daemon already does auxiliary completions (reflection/summary runs exist — `grep -rn "Transcript(" ~/Harnes --include="*.go" | grep -v _test` shows the pattern to copy; `session.Transcript(sess.History)` produces the transcript input, truncate to first 2000 chars).
- [ ] **Step 4: Run** — `go test ./session/ ./server/` → PASS. Full `go test ./...` + `go vet ./...` → PASS.
- [ ] **Step 5: Commit** — `git add session/ server/ cmd/ && git commit -m "session: stored LLM titles, newest-first listing; /v1/sessions exposes title+updated"`

---

### Task 8: Dart client — event ids + resume

**Files:**
- Modify: `~/dev/harness-app/lib/api/harness_client.dart` (chat parses `id:`; new `resume`), `~/dev/harness-app/lib/api/models.dart` (seq on events + `ResetEvent`)
- Test: `~/dev/harness-app/test/harness_client_test.dart`

**Interfaces:**
- Produces:
  - `ChatEvent.seq` (`int`, 0 when absent) — set by the client parser, not `parse()`.
  - `class ResetEvent extends ChatEvent {}` for `event: reset`.
  - `Stream<ChatEvent> resume({required String session, String? profile, int after = 0})` → `GET /v1/chat/stream`.
  - `HarnessApiException.status == 409` distinguishable (already is — `status` field).

- [ ] **Step 1: Failing tests** (the repo has `test/harness_client_test.dart` with a `MockClient` pattern — follow it):

```dart
test('chat surfaces seq from id: lines', () async {
  // MockClient streams: "id: 1\nevent: text\ndata: {\"delta\":\"hi\"}\n\n"
  // expect first event is TextDeltaEvent with seq == 1
});
test('resume hits /v1/chat/stream with after and parses reset', () async {
  // MockClient asserts path == /v1/chat/stream, query {session: s, after: '5'}
  // body: "event: reset\ndata: {}\n\n" → first event is ResetEvent
});
```

- [ ] **Step 2: Run** — `cd ~/dev/harness-app && flutter test test/harness_client_test.dart` → FAIL.
- [ ] **Step 3: Implement.** Extract the SSE line-loop in `chat()` into a private `Stream<ChatEvent> _sse(http.StreamedResponse res) async*` handling `id: ` lines (`seq = int.tryParse(...) ?? seq`), stamping `ev.seq = seq` (make `seq` a mutable field with default 0 on `ChatEvent`; sealed class keeps const constructors — so instead stamp via a wrapper: simplest is `int seq` non-final set post-parse; drop `const` where necessary). `resume()` builds the GET with query params and yields through `_sse`. `event: reset` case in `ChatEvent.parse` returns `ResetEvent()`.
- [ ] **Step 4: Run** — `flutter test` → PASS; `flutter analyze` → clean (fix anything it flags, including pre-existing — user rule: no dismissing warnings).
- [ ] **Step 5: Commit** — `git add lib/api test/ && git commit -m "client: SSE event ids + resume stream for detached turns"`

---

### Task 9: Chat screen — lifecycle resume, auto-retry, 409 attach

**Files:**
- Modify: `~/dev/harness-app/lib/screens/chat_screen.dart`

**Interfaces:**
- Consumes: Task 8's `resume`, `ChatEvent.seq`, `ResetEvent`, 409 `HarnessApiException`.
- Produces (behavioral contract):
  1. Screen mixes in `WidgetsBindingObserver`; `_lastSeq` updates on every event; `_turnActive` true from send until `DoneEvent`.
  2. `didChangeAppLifecycleState(resumed)` → if `_turnActive` → `_reattach()`.
  3. `_reattach()` = `client.resume(session: _session, profile: _profile, after: _lastSeq)`, events appended to the same in-progress assistant bubble; `ResetEvent` → refetch `sessionHistory` then re-render, continue stream.
  4. Stream error (`ClientException`/`SocketException`/`HarnessApiException` mid-stream) → retry `_reattach()` with backoff 1 s/2 s/4 s; only after 3 failures show the red error row (existing `_retry` UI stays as the manual fallback).
  5. Send returning 409 → `_reattach()` from seq in the error body when parseable, else 0.

- [ ] **Step 1: Read `chat_screen.dart` fully** (~750 lines) — locate `_send` (line ~264 calls `client.chat`), the message list state `_messages`, and the error rendering path. Map where `_turnActive`, `_lastSeq`, `_resumeAttempt` fields live (`State` class top).
- [ ] **Step 2: Implement** the five behaviors. Key snippet shape (adapt names to the real ones found in Step 1):

```dart
class _ChatScreenState extends State<ChatScreen> with WidgetsBindingObserver {
  int _lastSeq = 0;
  bool _turnActive = false;
  int _resumeAttempt = 0;
  StreamSubscription<ChatEvent>? _sub;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _sub?.cancel();
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed && _turnActive) _reattach();
  }

  void _listen(Stream<ChatEvent> stream, _Msg reply) {
    _sub?.cancel();
    _sub = stream.listen(
      (ev) {
        if (ev.seq > 0) _lastSeq = ev.seq;
        _resumeAttempt = 0;
        _apply(ev, reply); // existing per-event handling moves here unchanged
      },
      onError: (e) => _recover(reply),
      onDone: () {}, // done frame drives _turnActive=false inside _apply
    );
  }

  Future<void> _recover(_Msg reply) async {
    if (!_turnActive || _resumeAttempt >= 3) {
      setState(() => reply.error = true); // existing red-row path
      return;
    }
    await Future.delayed(Duration(seconds: 1 << _resumeAttempt));
    _resumeAttempt++;
    _reattach();
  }

  void _reattach() {
    final client = context.read<AppState>().client; // match existing accessor
    _listen(client.resume(session: _session, profile: _profile, after: _lastSeq), _liveReply());
  }
}
```

`ResetEvent` handling inside `_apply`: `await client.sessionHistory(...)` → rebuild `_messages` → keep listening. 409 on send: catch `HarnessApiException` where `status == 409` → `_reattach()`.
- [ ] **Step 3: Verify in simulator** — `cd ~/dev/harness-app && flutter run` against a local daemon (`cd ~/Harnes && go run . daemon` — check the actual daemon invocation in its README first). Send a message that triggers a slow tool, Cmd+Shift+H to background the sim app, wait 10 s, foreground → reply continues, no red error. Kill the daemon mid-turn → after 3 retries the red row appears.
- [ ] **Step 4: `flutter analyze`** → clean. **Step 5: Commit** — `git commit -am "chat: lifecycle-aware resume with backoff; attach to running turns on 409"`

---

### Task 10: Keychain token storage

**Files:**
- Modify: `~/dev/harness-app/pubspec.yaml`, wherever the token is read/written (`grep -rn "shared_preferences\|SharedPreferences" ~/dev/harness-app/lib` — expect `state/app_state.dart` and/or `connect_screen.dart`)

**Interfaces:**
- Produces: `class SecureStore { Future<String?> readToken(); Future<void> writeToken(String); Future<void> migrate(); }` in `lib/util/secure_store.dart`, wrapping `FlutterSecureStorage` with `kSecAttrAccessibleAfterFirstUnlock` (needed to read the token during background pushes later). `migrate()` moves a token found in SharedPreferences into Keychain, then removes it.

- [ ] **Step 1:** `flutter pub add flutter_secure_storage` (resolves latest; per user rule, versions come from the resolver not hardcoding).
- [ ] **Step 2: Implement + wire.** Call `migrate()` once at startup (before the first client build). All token reads/writes go through `SecureStore`. Server URL etc. can stay in SharedPreferences — only the bearer token moves.
- [ ] **Step 3: Test** — unit-test `migrate()` with `FlutterSecureStorage.setMockInitialValues` + `SharedPreferences.setMockInitialValues`; run `flutter test`.
- [ ] **Step 4:** Run in simulator: fresh install still connects (token re-entered), upgrade path keeps the token. **Step 5: Commit** — `git commit -am "app: bearer token moves to iOS Keychain with one-time migration"`

---

### Task 11: Sessions screen — titles, newest-first, continue

**Files:**
- Modify: `~/dev/harness-app/lib/api/models.dart` (`SessionMeta` gains `title`, `updated`), `~/dev/harness-app/lib/screens/sessions_screen.dart`
- Test: extend `test/harness_client_test.dart` (SessionMeta parsing)

- [ ] **Step 1:** Failing parse test: sessions JSON with `title`/`updated` → fields populated; missing title → falls back to existing preview text.
- [ ] **Step 2:** Implement: list tile shows `title` (bold) + relative `updated` (reuse `util/format.dart` if it has a relative-time helper — check first); server already sorts newest-first, keep client order as-received. Tap → existing `sessionHistory` load, and ensure the chat screen's session id binds to the tapped id so the composer continues that conversation (verify how chat_screen receives session today — `grep -n "session" lib/screens/sessions_screen.dart`).
- [ ] **Step 3:** `flutter test` + simulator check (titles appear after server Task 7 deploys locally). **Step 4: Commit** — `git commit -am "sessions: titled list, newest first, tap-to-continue"`

---

### Task 12: Connect Claude in Settings

**Files:**
- Modify: `~/dev/harness-app/lib/api/harness_client.dart` (2 methods), `~/dev/harness-app/lib/screens/settings_screen.dart`
- Test: extend `test/harness_client_test.dart`

**Interfaces:**
- Produces: `Future<({String url, String state})> claudeAuthStart({String? profile})`, `Future<void> claudeAuthComplete({required String code, required String state, String? profile})` (POSTs per Task 6 contract; non-200 → `HarnessApiException`).

- [ ] **Step 1:** Failing client tests (MockClient asserts paths + bodies, returns fixtures).
- [ ] **Step 2:** Implement client methods.
- [ ] **Step 3:** Settings UI: a "Claude" row in the connectors section (the screen already renders `/v1/connectors` — the server now includes claude there). Tapping when disconnected → `claudeAuthStart` → `launchUrl(url, mode: LaunchMode.externalApplication)` → dialog with a paste field ("Paste the code Claude shows you") → `claudeAuthComplete` → refresh connectors → row shows Connected.
- [ ] **Step 4:** `flutter test` + `flutter analyze` clean. Simulator: flow reaches the paste dialog (full exchange needs the deployed server — final verify happens in Task 15). **Step 5: Commit** — `git commit -am "settings: Connect Claude paste-code flow"`

---

### Task 13: App resilience polish — offline banner + list retries

**Files:**
- Modify: `~/dev/harness-app/lib/state/app_state.dart`, `~/dev/harness-app/lib/screens/home_shell.dart` (banner), list screens (inbox/tasks/sessions) as found

- [ ] **Step 1:** Read `app_state.dart`; add a periodic (30 s, only while app active) + on-resume `health()` probe exposing `bool online`. Banner: thin amber strip under the app bar in `home_shell.dart` — "Offline — reconnecting…" when `!online`.
- [ ] **Step 2:** Each list screen's load: on error show the existing empty/error state with a Retry button rather than silent emptiness (check each screen; several already have pull-to-refresh — keep patterns consistent).
- [ ] **Step 3:** `flutter analyze` clean; simulator: kill daemon → banner appears; restart → clears. **Step 4: Commit** — `git commit -am "app: offline banner + consistent list retry"`

---

### Task 14: Whole-app review pass + fixes

- [ ] **Step 1:** Use the superpowers:requesting-code-review skill: dispatch a review of `~/Harnes` changes (`git log --oneline main@{u}..` scope, plus a targeted race/leak read of `server/`, `auth/`) and a whole-repo review of `~/dev/harness-app` (unawaited futures, `setState` after dispose, stream/subscription leaks, error swallowing).
- [ ] **Step 2:** Fix all findings labeled bug/leak/race; fold style-only notes in where trivial. Re-run `go test -race ./...` and `flutter test` + `flutter analyze`.
- [ ] **Step 3:** Commit fixes per finding-group — `git commit -m "review: <finding>"` in each repo.

---

### Task 15: Ship the server (donna deploy) + seed-auth fallback

**Files:**
- Modify: `~/dev/donna/deploy.sh` (add `--seed-auth`), `~/dev/donna/Dockerfile` (pin new harness release)

- [ ] **Step 1:** In `~/Harnes`: `go test ./... && go vet ./...` → clean; tag: `git tag v<next> && git push origin main --tags` (check existing tag scheme: `git tag | tail -3`).
- [ ] **Step 2:** `deploy.sh --seed-auth`: after the existing env re-apply, `bp cp ~/Harnes/auth.json <app>:/data/auth.json && bp exec <app> chmod 600 /data/auth.json` — match the transfer mechanism `deploy.sh` already uses for `.api-token` (read it first; if Basepod has no file-copy verb, mount via the existing secret/env path instead and document that inside the script).
- [ ] **Step 3:** Update Dockerfile's pinned harness version to the new tag. `./deploy.sh`. Verify: `curl -s https://donna.common.al/healthz` → ok; `curl -s -X POST https://donna.common.al/v1/auth/claude/start -H "Authorization: Bearer <token>"` → URL; from the app (simulator, pointed at prod): run Connect Claude end-to-end, pick claude provider, send a message → reply streams from Claude.
- [ ] **Step 4:** Background-switch test against prod from the simulator: send → background 15 s → foreground → turn resumed. THE original bug is dead.
- [ ] **Step 5:** Commit donna changes — `git commit -am "deploy: harness v<next> with detached turns + claude oauth; --seed-auth fallback"` and push.

---

### Task 16: TestFlight release

**Files:**
- Create: `~/dev/harness-app/release.sh`

- [ ] **Step 1:** Confirm credential with the user: `ASC_KEY_ID`/`ASC_ISSUER_ID`/`ASC_KEY_PATH` env set, **or** keychain item `AC_PASSWORD` (`security add-generic-password -a dev@basecode.al -s AC_PASSWORD -w <app-specific password>`). Do not proceed silently without one.
- [ ] **Step 2:** Write `release.sh`:

```bash
#!/usr/bin/env bash
# Release to TestFlight: bump build, build signed IPA, upload.
# Credentials (either):
#   ASC_KEY_ID + ASC_ISSUER_ID + ASC_KEY_PATH   App Store Connect API key
#   keychain item AC_PASSWORD                    app-specific password for dev@basecode.al
set -euo pipefail
cd "$(dirname "$0")"

# bump the +N build number in pubspec.yaml
ver=$(grep '^version:' pubspec.yaml | awk '{print $2}')
base=${ver%+*}; build=${ver#*+}; next=$((build + 1))
sed -i '' "s/^version: .*/version: ${base}+${next}/" pubspec.yaml
echo "building ${base}+${next}"

flutter build ipa --release --export-method app-store

ipa=(build/ios/ipa/*.ipa)
if [[ -n "${ASC_KEY_ID:-}" ]]; then
  mkdir -p ~/.appstoreconnect/private_keys
  cp -n "${ASC_KEY_PATH}" ~/.appstoreconnect/private_keys/AuthKey_${ASC_KEY_ID}.p8 2>/dev/null || true
  xcrun altool --upload-app --type ios -f "${ipa[0]}" \
    --apiKey "${ASC_KEY_ID}" --apiIssuer "${ASC_ISSUER_ID}"
else
  xcrun altool --upload-app --type ios -f "${ipa[0]}" \
    -u dev@basecode.al -p "@keychain:AC_PASSWORD"
fi

git add pubspec.yaml && git commit -m "release: build ${base}+${next}"
echo "uploaded — check App Store Connect → TestFlight (processing takes ~10 min)"
```

`chmod +x release.sh`; commit the script itself first: `git add release.sh && git commit -m "release: TestFlight upload script (ASC key or app-specific password)"`.
- [ ] **Step 3:** Point the app's default server config at `https://donna.common.al` (check `lib/config.dart` — it's gitignored? `config.example.dart` exists; make sure the release build uses the prod URL) and `./release.sh`.
- [ ] **Step 4:** Expected: `UPLOAD SUCCEEDED`. Then user actions in App Store Connect → TestFlight: answer export compliance (HTTPS-only → exempt), enable internal testing, add dev@basecode.al as tester → install from the TestFlight app on the phone.
- [ ] **Step 5:** On-device confirmation of the headline fix: send a long-running message, switch apps, come back → reply intact. Also propose creating a GitHub remote for `harness-app` (it has none — TestFlight-shipped code should be backed up) — ask the user before creating.

---

## Self-review notes

- Spec coverage: §1→Tasks 2-4, 8-9; §2→Tasks 1, 5, 14; §3→Tasks 6, 12, 15; §4→Tasks 7, 11; §5→Task 16; error-handling table→Tasks 3 (timeout frame), 4 (`reset`), 6 (400/502 paths), 5+13 (rate limit/offline UX). Web UI reconnect-without-replay: EventSource auto-reconnects to `/v1/chat/stream`? — the web UI posts to /v1/chat; unchanged behavior is acceptable per spec (out of scope).
- Known judgment calls for implementers: exact mock-blocking wiring in Task 4 tests follows whatever `server_test.go` already does; Titler's `runOneShot` copies the daemon's existing auxiliary-completion pattern; Dart `seq` field mutability may force dropping `const` constructors on events (fine — they're value carriers).
- Deliberately not in plan: brute-force lockout, session search/pin, Android, WebSockets (spec out-of-scope).
