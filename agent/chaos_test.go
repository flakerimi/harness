package agent

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/tool"
)

// chaosProvider streams a randomized fault on every call — the compressed
// version of everything production threw at the loop in its first days:
// timeouts, garbage tool JSON, empty tool names, output-limit truncation,
// rate limits, empty replies. Eventually (or when the budget forces a
// no-tools wrap-up call) it answers with text so runs can terminate.
type chaosProvider struct {
	r     *rand.Rand
	calls int
}

func (c *chaosProvider) Name() string { return "chaos" }

func (c *chaosProvider) Stream(_ context.Context, req provider.Request, emit func(provider.Event)) error {
	c.calls++
	// The wrap-up call (no tools offered) must answer with text: the contract
	// is "budget exhausted, answer now" — even chaos respects max_tokens-free
	// plain text here, or returns an error.
	if len(req.Tools) == 0 {
		if c.r.Intn(6) == 0 {
			return errors.New("dial tcp 1.2.3.4:443: i/o timeout")
		}
		emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "wrap-up answer"})
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
		return nil
	}

	switch c.r.Intn(9) {
	case 0: // transport failure
		return errors.New("dial tcp 1.2.3.4:443: i/o timeout")
	case 1: // rate limited
		return errors.New("HTTP 429: rate limited")
	case 2: // garbage tool-call JSON
		emit(provider.Event{Type: provider.EventToolUseStart, Index: 0, ToolUseID: "g1", ToolName: "echo"})
		emit(provider.Event{Type: provider.EventToolUseDelta, Index: 0, InputDelta: `{"msg": "unclosed...`})
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopToolUse})
	case 3: // tool call with empty name
		emit(provider.Event{Type: provider.EventToolUseStart, Index: 0, ToolUseID: "e1", ToolName: ""})
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	case 4: // output limit cuts the call mid-arguments
		emit(provider.Event{Type: provider.EventToolUseStart, Index: 0, ToolUseID: "m1", ToolName: "echo"})
		emit(provider.Event{Type: provider.EventToolUseDelta, Index: 0, InputDelta: `{"msg": "a huge unfinished`})
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopMaxTokens})
	case 5: // empty reply
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	case 6: // unknown tool requested
		emit(provider.Event{Type: provider.EventToolUseStart, Index: 0, ToolUseID: "u1", ToolName: "no_such_tool"})
		emit(provider.Event{Type: provider.EventToolUseDelta, Index: 0, InputDelta: `{}`})
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopToolUse})
	case 7: // healthy tool call
		emit(provider.Event{Type: provider.EventToolUseStart, Index: 0, ToolUseID: "ok1", ToolName: "echo"})
		emit(provider.Event{Type: provider.EventToolUseDelta, Index: 0, InputDelta: `{"msg":"hello"}`})
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopToolUse})
	default: // healthy final answer
		emit(provider.Event{Type: provider.EventTextDelta, TextDelta: "final answer"})
		emit(provider.Event{Type: provider.EventStop, StopReason: provider.StopEndTurn})
	}
	return nil
}

// TestChaosLoopInvariants runs the full loop against hundreds of fault
// sequences and asserts the production contract: the loop always returns —
// with either usable output or an error — never panics, never runs away, and
// always leaves a replayable history (repair-idempotent, so persisting it
// can't wedge the session).
func TestChaosLoopInvariants(t *testing.T) {
	for seed := range 300 {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			prov := &chaosProvider{r: rand.New(rand.NewSource(int64(seed)))}
			reg := tool.NewRegistry()
			reg.Register(&echoTool{})
			ag := New(prov, reg, Options{MaxTurns: 6})

			var out strings.Builder
			history, err := ag.Continue(context.Background(), nil, "do the chaotic thing", &collectHandler{out: &out})

			// Invariant 1: an answer or an error — never a silent nothing
			// after a run that used turns.
			if err == nil && strings.TrimSpace(out.String()) == "" && prov.calls > 1 {
				t.Errorf("no output and no error after %d provider calls", prov.calls)
			}
			// Invariant 2: bounded work — MaxTurns + wrap-up at most.
			if prov.calls > 8 {
				t.Errorf("runaway loop: %d provider calls", prov.calls)
			}
			// Invariant 3: whatever happened, the history must be safe to
			// persist and replay: repairing it changes nothing.
			repaired := RepairHistory(history)
			if len(repaired) != len(history) {
				t.Errorf("returned history is not replay-safe: %d msgs → %d after repair", len(history), len(repaired))
			}
		})
	}
}
