package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	journal  *journal
	subs     map[chan frame]struct{}
	running  bool
	finished time.Time // when the last turn ended; drives idle eviction
}

// turnManager runs agent turns detached from any HTTP connection — the fix for
// iOS backgrounding killing turns mid-run. A client disconnect only removes a
// subscriber; the turn finishes on its own context and timeout.
type turnManager struct {
	mu         sync.Mutex
	turns      map[string]*turnState
	active     int
	max        int
	timeout    time.Duration
	journalCap int
}

func newTurnManager(maxConcurrent int, timeout time.Duration) *turnManager {
	return &turnManager{
		turns: map[string]*turnState{}, max: maxConcurrent,
		timeout: timeout, journalCap: 512,
	}
}

// state returns the key's turnState, creating it on first use. Callers hold mu.
func (m *turnManager) state(key string) *turnState {
	st, ok := m.turns[key]
	if !ok {
		st = &turnState{journal: newJournal(m.journalCap), subs: map[chan frame]struct{}{}}
		m.turns[key] = st
	}
	return st
}

// Start launches run on a fresh goroutine bound to a background context. The
// handler it receives journals every event and fans it out to subscribers.
// The returned seq is the journal cursor at turn start — stream from there so
// a fresh POST never replays (or resets over) the previous turn's frames.
func (m *turnManager) Start(key string, run func(ctx context.Context, h agent.Handler)) (uint64, error) {
	m.mu.Lock()
	st := m.state(key)
	if st.running {
		m.mu.Unlock()
		return 0, errTurnBusy
	}
	if m.active >= m.max {
		m.mu.Unlock()
		return 0, errTurnCapacity
	}
	st.running = true
	st.journal.Reset()
	st.journal.mu.Lock()
	startSeq := st.journal.seq
	st.journal.mu.Unlock()
	m.active++
	m.evictIdleLocked()
	m.mu.Unlock()

	h := &journalHandler{m: m, key: key}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
		defer cancel()
		defer func() {
			m.mu.Lock()
			st.running = false
			st.finished = time.Now()
			m.active--
			for ch := range st.subs {
				close(ch)
				delete(st.subs, ch)
			}
			m.mu.Unlock()
		}()
		// A panic in provider streaming or a tool must not take down the
		// daemon: the old per-connection handler had net/http's recovery,
		// a detached goroutine has only this one.
		defer func() {
			if r := recover(); r != nil {
				m.emit(key, "error", map[string]any{"error": fmt.Sprintf("turn panicked: %v", r)})
				m.emit(key, "done", map[string]any{"turns": 0})
			}
		}()
		run(ctx, h)
	}()
	return startSeq, nil
}

// evictIdleLocked drops long-finished turn states so probing many session ids
// can't grow the map forever. Callers hold m.mu.
func (m *turnManager) evictIdleLocked() {
	if len(m.turns) <= 64 {
		return
	}
	for k, st := range m.turns {
		if !st.running && !st.finished.IsZero() && time.Since(st.finished) > 30*time.Minute {
			delete(m.turns, k)
		}
	}
}

// Subscribe attaches to a session's turn: replay first (frames > after), then
// read live until closed. live is nil when no turn is running — the replay
// still carries a finished turn's tail, ending with its done frame. Unknown
// keys allocate nothing: a stream probe must not grow the map.
func (m *turnManager) Subscribe(key string, after uint64) (replay []frame, evicted bool, live <-chan frame, cancel func(), running bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.turns[key]
	if !ok {
		return nil, false, nil, func() {}, false
	}
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

// Running reports whether the key has a live turn and its latest journal seq.
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

// journalHandler adapts agent.Handler (and RouteAware) onto the manager's emit,
// replacing the old per-connection sseHandler.
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
