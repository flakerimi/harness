package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/router"
)

const (
	defaultKeepTurns = 4
	// charsPerToken is a coarse token estimate — good enough for budgeting.
	charsPerToken = 4
)

// assemble produces the messages to send this turn: the context strategy's
// selection, then summarizing compaction if the result still exceeds the token
// budget. It never mutates the retained history.
func (a *Agent) assemble(ctx context.Context, history []provider.Message) []provider.Message {
	msgs := a.strategy().Assemble(history)
	budget := a.opts.CompactTokens
	if budget <= 0 || estimateTokens(msgs) <= budget {
		return msgs
	}
	keep := a.opts.KeepTurns
	if keep <= 0 {
		keep = defaultKeepTurns
	}
	prefix, tail := splitForCompaction(msgs, keep)
	if len(prefix) == 0 {
		return msgs // already at the minimum we can keep
	}
	summary := a.compactSummary(ctx, prefix)
	if summary == "" {
		return msgs // summarization failed → send the full history rather than lose it
	}
	// Inject the summary as a clean user→assistant pair so role alternation is
	// preserved and the kept tail (which starts on a user turn) follows naturally.
	out := make([]provider.Message, 0, len(tail)+2)
	out = append(out,
		provider.Message{Role: "user", Content: []provider.Block{{
			Type: provider.BlockText,
			Text: "Summary of our earlier conversation (for context):\n" + summary,
		}}},
		provider.Message{Role: "assistant", Content: []provider.Block{{
			Type: provider.BlockText,
			Text: "Got it — I'll keep that context in mind.",
		}}},
	)
	out = append(out, tail...)
	return out
}

// compactSummary returns a cached summary when prefix covers the same number of
// messages as a previous call (so it isn't recomputed every turn), else it
// summarizes afresh and caches the result.
func (a *Agent) compactSummary(ctx context.Context, prefix []provider.Message) string {
	if a.sumCount == len(prefix) && a.sumText != "" {
		return a.sumText
	}
	s := a.summarize(ctx, renderForSummary(prefix))
	if s != "" {
		a.sumText = s
		a.sumCount = len(prefix)
	}
	return s
}

// summarize asks the fast-tier model to condense text. On any error it returns
// "" so the caller falls back to sending the full (uncompacted) history.
func (a *Agent) summarize(ctx context.Context, text string) string {
	var sb strings.Builder
	err := a.prov.Stream(ctx, provider.Request{
		Model:     a.modelFor(router.TierFast),
		MaxTokens: 1024,
		System:    "You compress a conversation excerpt into a concise brief that preserves every fact, decision, name, number, preference, and open thread needed to continue. Use tight bullet points. No preamble, no commentary.",
		Messages:  []provider.Message{{Role: "user", Content: []provider.Block{{Type: provider.BlockText, Text: text}}}},
	}, func(ev provider.Event) {
		if ev.Type == provider.EventTextDelta {
			sb.WriteString(ev.TextDelta)
		}
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(sb.String())
}

// splitForCompaction divides history into a prefix to summarize and a tail to
// keep verbatim. The tail begins at the keepTurns-th user message from the end,
// so it always starts on a turn boundary — never an orphan tool_result whose
// matching tool_use was summarized away.
func splitForCompaction(history []provider.Message, keepTurns int) (prefix, tail []provider.Message) {
	if keepTurns < 1 {
		keepTurns = 1
	}
	seen, cut := 0, -1
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			seen++
			if seen == keepTurns {
				cut = i
				break
			}
		}
	}
	if cut <= 0 {
		return nil, history // everything is within the kept turns
	}
	return history[:cut], history[cut:]
}

// renderForSummary flattens messages into plain text for the summarizer.
func renderForSummary(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		for _, blk := range m.Content {
			switch {
			case blk.Text != "":
				fmt.Fprintf(&b, "%s: %s\n", m.Role, blk.Text)
			case blk.ToolUse != nil:
				fmt.Fprintf(&b, "%s called %s(%v)\n", m.Role, blk.ToolUse.Name, blk.ToolUse.Input)
			case blk.ToolResult != nil:
				fmt.Fprintf(&b, "tool result: %s\n", truncate(blk.ToolResult.Content, 500))
			}
		}
	}
	return b.String()
}

// estimateTokens approximates the token footprint of messages (chars / 4).
func estimateTokens(msgs []provider.Message) int {
	chars := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			chars += len(b.Text)
			if b.ToolUse != nil {
				chars += len(b.ToolUse.Name)
				for k, v := range b.ToolUse.Input {
					chars += len(k) + len(fmt.Sprint(v))
				}
			}
			if b.ToolResult != nil {
				chars += len(b.ToolResult.Content)
			}
		}
	}
	return chars / charsPerToken
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
