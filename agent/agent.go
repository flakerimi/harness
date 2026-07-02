// Package agent is the provider-agnostic engine: it drives the model turn,
// accumulates streamed events into message blocks, dispatches tool calls, and
// loops until the model stops. It never imports a vendor SDK — it talks only
// to the neutral provider.Provider and the tool.Registry.
package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/router"
	"github.com/flakerimi/harness/tool"
)

// Handler receives the agent's output as it happens. A CLI prints it; a web
// transport forwards it over SSE; a test records it. The engine doesn't care.
type Handler interface {
	OnText(delta string)
	OnToolStart(name, id string)
	OnToolResult(name string, res tool.Result)
	OnUsage(u provider.Usage)
	OnStop(reason string)
}

// Options configure an Agent.
type Options struct {
	Model      string
	System     string
	MaxTokens  int
	Caps       []string        // provider capability flags (see provider.Cap*)
	Env        *tool.Env       // mediated world for tools; defaults to root "."
	MaxTurns   int             // safety cap on the call→result loop; default 16
	Context    ContextStrategy // what to send each turn; defaults to KeepAll
	Permission PermissionGate  // optional gate run before each tool call

	// Compaction: when CompactTokens > 0 and the assembled history's estimated
	// size exceeds it, the oldest complete turns are summarized by a fast-tier
	// call into one synthetic exchange so long chats stay within the context
	// window. The persisted history is untouched — only the prompt is compacted.
	CompactTokens int
	KeepTurns     int // recent user-turns kept verbatim when compacting; default 4

	// Model routing. When Model is empty and Router is set, the loop resolves a
	// model per turn from the call's tier. Model (if set) overrides routing.
	Router   router.Router // (provider, tier) → model
	BaseTier router.Tier   // starting tier; defaults to reasoning
	Escalate bool          // retry a turn one tier up when it produces nothing usable
	Classify bool          // pre-classify the task (a fast-tier call) to pick BaseTier

	// Self-critique. When Critique is set, a final answer is reviewed by a critic
	// pass before it's returned; if the critic finds concrete problems, the loop
	// runs once more to revise (up to MaxCritique passes, default 1). This is the
	// in-task quality loop — it makes a single answer better, not the agent
	// smarter over time.
	Critique    bool
	MaxCritique int
}

// RouteAware is an optional Handler extension told which tier/model runs each
// turn — handy for surfacing automatic model switching.
type RouteAware interface {
	OnRoute(tier, model string)
}

// CritiqueAware is an optional Handler extension told when a critic pass sends
// the answer back for revision — so a surface can show "revising…".
type CritiqueAware interface {
	OnCritique(pass int, issues string)
}

// Agent binds a Provider and a tool Registry into a runnable loop.
type Agent struct {
	prov  provider.Provider
	tools *tool.Registry
	opts  Options

	// compaction summary cache: the summary text and the number of leading
	// history messages it covers, so a long chat re-summarizes only when the
	// summarized prefix actually grows.
	sumText  string
	sumCount int
}

// New constructs an Agent.
func New(prov provider.Provider, tools *tool.Registry, opts Options) *Agent {
	return &Agent{prov: prov, tools: tools, opts: opts}
}

// Run drives the conversation from a single user message until the model stops
// for a non-tool reason (or the turn cap is hit). It is a stateless one-shot —
// for a persisted multi-turn conversation use Continue.
func (a *Agent) Run(ctx context.Context, userInput string, h Handler) error {
	_, err := a.Continue(ctx, nil, userInput, h)
	return err
}

// Continue resumes a conversation: it appends the user's message to the prior
// history, drives the loop until the model stops, and returns the full updated
// history (user + assistant + tool messages). A session store can persist the
// result and pass it back next turn for true multi-turn memory. On error it
// returns the history accumulated so far so nothing is silently lost.
func (a *Agent) Continue(ctx context.Context, history []provider.Message, userInput string, h Handler) ([]provider.Message, error) {
	msgs := make([]provider.Message, 0, len(history)+2)
	msgs = append(msgs, history...)
	msgs = append(msgs, provider.Message{
		Role:    "user",
		Content: []provider.Block{{Type: provider.BlockText, Text: userInput}},
	})

	tier := a.opts.BaseTier
	if tier == "" {
		tier = router.TierReasoning
	}
	if a.routingOn() && a.opts.Classify {
		tier = a.classify(ctx, userInput, tier)
	}

	maxTurns := a.opts.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 16
	}
	critiques := 0

	for range maxTurns {
		model := a.modelFor(tier)
		a.notifyRoute(h, tier, model)

		assistant, stop, err := a.streamOne(ctx, msgs, model, h)
		if err != nil {
			return msgs, err
		}

		// Escalate: a turn that produced nothing usable retries one tier up.
		if a.routingOn() && a.opts.Escalate && tier != router.TierReasoning && fumbled(assistant) {
			tier = router.Escalate(tier)
			continue
		}

		msgs = append(msgs, assistant)
		if stop != provider.StopToolUse {
			// Self-critique: before accepting a final answer, optionally have a
			// critic check it. If it finds concrete problems, feed them back and
			// loop once more to revise — bounded by MaxCritique.
			if a.opts.Critique && stop == provider.StopEndTurn && critiques < a.maxCritique() {
				if issues := a.critique(ctx, userInput, assistant); issues != "" {
					critiques++
					if ca, ok := h.(CritiqueAware); ok {
						ca.OnCritique(critiques, issues)
					}
					msgs = append(msgs, provider.Message{
						Role:    "user",
						Content: []provider.Block{{Type: provider.BlockText, Text: revisePrompt(issues)}},
					})
					continue
				}
			}
			h.OnStop(stop)
			return msgs, nil
		}

		results := a.runTools(ctx, assistant, h)
		msgs = append(msgs, provider.Message{Role: "tool", Content: results})
	}

	// Tool budget exhausted mid-work. Don't throw the work away — run one
	// final no-tools turn so the model wraps up with what it has gathered.
	msgs = append(msgs, provider.Message{
		Role:    "user",
		Content: []provider.Block{{Type: provider.BlockText, Text: "(Tool budget for this request is exhausted — no more tool calls will run. Give your best final answer now from what you've already gathered, and note plainly anything left unfinished.)"}},
	})
	model := a.modelFor(tier)
	a.notifyRoute(h, tier, model)
	assistant, stop, err := a.streamFinal(ctx, msgs, model, h)
	if err != nil {
		return msgs, err
	}
	msgs = append(msgs, assistant)
	h.OnStop(stop)
	return msgs, nil
}

func (a *Agent) routingOn() bool {
	return a.opts.Model == "" && a.opts.Router != nil
}

// modelFor resolves the model for a tier: explicit override wins, then the
// router, then the provider's default.
func (a *Agent) modelFor(tier router.Tier) string {
	if a.opts.Model != "" {
		return a.opts.Model
	}
	if a.opts.Router != nil {
		if c := a.opts.Router.Resolve(a.prov.Name(), tier); c.Model != "" {
			return c.Model
		}
	}
	return provider.DefaultModel(a.prov.Name())
}

// classify asks the fast-tier model to label the task's difficulty, returning
// fallback on any error or ambiguity (so it never silently under-powers).
func (a *Agent) classify(ctx context.Context, task string, fallback router.Tier) router.Tier {
	var sb strings.Builder
	err := a.prov.Stream(ctx, provider.Request{
		Model:     a.modelFor(router.TierFast),
		MaxTokens: 16,
		System:    "Classify the difficulty of the user's task for model routing. Reply with exactly one word: fast, balanced, or reasoning. Use 'fast' for simple lookups, extraction, or formatting; 'reasoning' for hard multi-step planning, analysis, or coding; 'balanced' otherwise.",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: provider.BlockText, Text: task}}}},
	}, func(ev provider.Event) {
		if ev.Type == provider.EventTextDelta {
			sb.WriteString(ev.TextDelta)
		}
	})
	if err != nil {
		return fallback
	}
	return router.ParseTier(sb.String(), fallback)
}

func (a *Agent) notifyRoute(h Handler, tier router.Tier, model string) {
	if ra, ok := h.(RouteAware); ok {
		ra.OnRoute(string(tier), model)
	}
}

func (a *Agent) maxCritique() int {
	if a.opts.MaxCritique > 0 {
		return a.opts.MaxCritique
	}
	return 1
}

// critique asks a critic model to review the assistant's answer against the
// task. It returns concrete problems to fix, or "" when the answer passes (or
// on any error — a critic must never block a usable answer). The critic runs at
// the reasoning tier so a weaker worker's answer is checked by a strong model.
func (a *Agent) critique(ctx context.Context, task string, answer provider.Message) string {
	ans := messageText(answer)
	if strings.TrimSpace(ans) == "" {
		return ""
	}
	var sb strings.Builder
	err := a.prov.Stream(ctx, provider.Request{
		Model:     a.modelFor(router.TierReasoning),
		MaxTokens: 512,
		System:    "You are a strict reviewer. Check the assistant's ANSWER against the TASK for correctness, completeness, and whether it did what was asked. If it is good, reply with exactly: OK. Otherwise list the concrete problems to fix as short bullet points — no preamble, no praise.",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: provider.BlockText, Text: "TASK:\n" + task + "\n\nANSWER:\n" + ans}}}},
	}, func(ev provider.Event) {
		if ev.Type == provider.EventTextDelta {
			sb.WriteString(ev.TextDelta)
		}
	})
	if err != nil {
		return "" // fail open
	}
	out := strings.TrimSpace(sb.String())
	if out == "" || approved(out) {
		return ""
	}
	return out
}

// approved reports whether a critic reply signals "no problems" (a bare OK,
// case- and punctuation-insensitive).
func approved(s string) bool {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.TrimRight(s, ".!")
	return s == "OK" || s == "LGTM"
}

func revisePrompt(issues string) string {
	return "Revise your previous answer. A reviewer found these problems:\n" + issues +
		"\n\nProduce an improved answer that fixes them. Do not mention this review."
}

// messageText joins the text blocks of a message.
func messageText(m provider.Message) string {
	var parts []string
	for _, b := range m.Content {
		if b.Type == provider.BlockText && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// fumbled reports a turn that produced nothing usable (empty assistant message)
// — the signal to escalate to a stronger tier.
func fumbled(m provider.Message) bool {
	return len(m.Content) == 0
}

// streamOne runs one model turn, accumulating streamed events into an assistant
// message. Tool calls are tracked per content-block index so parallel calls
// from a single turn don't get interleaved into one another.
func (a *Agent) streamOne(ctx context.Context, msgs []provider.Message, model string, h Handler) (provider.Message, string, error) {
	return a.stream(ctx, msgs, model, h, a.providerTools())
}

// streamFinal is the budget-exhausted wrap-up call: no tools offered, so the
// model must answer with what it has.
func (a *Agent) streamFinal(ctx context.Context, msgs []provider.Message, model string, h Handler) (provider.Message, string, error) {
	return a.stream(ctx, msgs, model, h, nil)
}

func (a *Agent) stream(ctx context.Context, msgs []provider.Message, model string, h Handler, provTools []provider.Tool) (provider.Message, string, error) {
	req := provider.Request{
		Model:     model,
		System:    a.opts.System,
		Messages:  a.assemble(ctx, msgs),
		Tools:     provTools,
		MaxTokens: a.opts.MaxTokens,
		CapFlags:  a.opts.Caps,
	}

	var textBuf strings.Builder
	tools := map[int]*toolAccum{}
	var order []int
	stop := ""

	accum := func(idx int) *toolAccum {
		ta, ok := tools[idx]
		if !ok {
			ta = &toolAccum{}
			tools[idx] = ta
			order = append(order, idx)
		}
		return ta
	}

	err := a.prov.Stream(ctx, req, func(ev provider.Event) {
		switch ev.Type {
		case provider.EventTextDelta:
			textBuf.WriteString(ev.TextDelta)
			h.OnText(ev.TextDelta)
		case provider.EventToolUseStart:
			ta := accum(ev.Index)
			ta.id, ta.name = ev.ToolUseID, ev.ToolName
			h.OnToolStart(ev.ToolName, ev.ToolUseID)
		case provider.EventToolUseDelta:
			accum(ev.Index).input.WriteString(ev.InputDelta)
		case provider.EventUsage:
			h.OnUsage(ev.Usage)
		case provider.EventStop:
			stop = ev.StopReason
		}
	})
	if err != nil {
		return provider.Message{}, "", err
	}

	var blocks []provider.Block
	if textBuf.Len() > 0 {
		blocks = append(blocks, provider.Block{Type: provider.BlockText, Text: textBuf.String()})
	}
	slices.Sort(order)
	for _, idx := range order {
		ta := tools[idx]
		var input map[string]any
		if s := strings.TrimSpace(ta.input.String()); s != "" {
			_ = json.Unmarshal([]byte(s), &input)
		}
		blocks = append(blocks, provider.Block{
			Type:    provider.BlockToolUse,
			ToolUse: &provider.ToolUseBlock{ID: ta.id, Name: ta.name, Input: input},
		})
	}

	if stop == "" {
		stop = provider.StopEndTurn
	}
	return provider.Message{Role: "assistant", Content: blocks}, stop, nil
}

func (a *Agent) runTools(ctx context.Context, assistant provider.Message, h Handler) []provider.Block {
	var results []provider.Block
	for _, b := range assistant.Content {
		if b.Type != provider.BlockToolUse || b.ToolUse == nil {
			continue
		}
		tu := b.ToolUse
		res := a.runOne(ctx, tu, h)
		results = append(results, provider.Block{
			Type: provider.BlockToolResult,
			ToolResult: &provider.ToolResultBlock{
				ToolUseID: tu.ID,
				Content:   capToolResult(res.Content),
				IsError:   res.IsError,
			},
		})
	}
	return results
}

func (a *Agent) runOne(ctx context.Context, tu *provider.ToolUseBlock, h Handler) tool.Result {
	t, ok := a.tools.Get(tu.Name)
	if !ok {
		res := tool.Result{Content: "unknown tool: " + tu.Name, IsError: true}
		h.OnToolResult(tu.Name, res)
		return res
	}
	input, _ := json.Marshal(tu.Input)

	if a.opts.Permission != nil && a.opts.Permission(ctx, tu.Name, input) == Deny {
		res := tool.Result{Content: "permission denied for tool: " + tu.Name, IsError: true}
		h.OnToolResult(tu.Name, res)
		return res
	}

	res, err := t.Run(ctx, input, a.env())
	if err != nil {
		res = tool.Result{Content: err.Error(), IsError: true}
	}
	h.OnToolResult(tu.Name, res)
	return res
}

func (a *Agent) providerTools() []provider.Tool {
	specs := a.tools.Specs()
	out := make([]provider.Tool, 0, len(specs))
	for _, s := range specs {
		out = append(out, provider.Tool{Name: s.Name, Description: s.Description, InputSchema: s.InputSchema})
	}
	return out
}

func (a *Agent) env() *tool.Env {
	if a.opts.Env != nil {
		return a.opts.Env
	}
	return &tool.Env{Root: "."}
}

// Env exposes the mediated world the agent hands its tools — surfaces use it
// to show where the agent is rooted (e.g. an identity's workspace).
func (a *Agent) Env() *tool.Env {
	return a.env()
}

func (a *Agent) strategy() ContextStrategy {
	if a.opts.Context != nil {
		return a.opts.Context
	}
	return KeepAll{}
}

type toolAccum struct {
	id    string
	name  string
	input strings.Builder
}
