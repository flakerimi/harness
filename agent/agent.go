// Package agent is the provider-agnostic engine: it drives the model turn,
// accumulates streamed events into message blocks, dispatches tool calls, and
// loops until the model stops. It never imports a vendor SDK — it talks only
// to the neutral provider.Provider and the tool.Registry.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flakerimi/harness/provider"
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
	Model     string
	System    string
	MaxTokens int
	Caps      []string  // provider capability flags (see provider.Cap*)
	Env       *tool.Env // mediated world for tools; defaults to root "."
	MaxTurns  int       // safety cap on the call→result loop; default 16
}

// Agent binds a Provider and a tool Registry into a runnable loop.
type Agent struct {
	prov  provider.Provider
	tools *tool.Registry
	opts  Options
}

// New constructs an Agent.
func New(prov provider.Provider, tools *tool.Registry, opts Options) *Agent {
	return &Agent{prov: prov, tools: tools, opts: opts}
}

// Run drives the conversation from a single user message until the model stops
// for a non-tool reason (or the turn cap is hit).
func (a *Agent) Run(ctx context.Context, userInput string, h Handler) error {
	msgs := []provider.Message{{
		Role:    "user",
		Content: []provider.Block{{Type: provider.BlockText, Text: userInput}},
	}}

	maxTurns := a.opts.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 16
	}

	for range maxTurns {
		assistant, stop, err := a.streamOne(ctx, msgs, h)
		if err != nil {
			return err
		}
		msgs = append(msgs, assistant)

		if stop != provider.StopToolUse {
			h.OnStop(stop)
			return nil
		}

		results := a.runTools(ctx, assistant, h)
		msgs = append(msgs, provider.Message{Role: "tool", Content: results})
	}
	return fmt.Errorf("agent: exceeded %d turns", maxTurns)
}

// streamOne runs one model turn, accumulating streamed events into an assistant
// message. Tool calls are tracked per content-block index so parallel calls
// from a single turn don't get interleaved into one another.
func (a *Agent) streamOne(ctx context.Context, msgs []provider.Message, h Handler) (provider.Message, string, error) {
	req := provider.Request{
		Model:     a.opts.Model,
		System:    a.opts.System,
		Messages:  msgs,
		Tools:     a.providerTools(),
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
				Content:   res.Content,
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

type toolAccum struct {
	id    string
	name  string
	input strings.Builder
}
