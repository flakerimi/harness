package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"

	"github.com/flakerimi/harness/provider"
)

// ContextStrategy decides which messages to send to the model each turn — the
// pluggable hook for windowing, trimming, and (later) compaction. It is pure:
// given the full history, it returns the messages to send. The loop retains the
// complete history regardless of what the strategy returns. Shipping a strong
// default here is what separates a usable harness from a toy; swap it for a
// domain-specific one when you need to.
type ContextStrategy interface {
	Assemble(history []provider.Message) []provider.Message
}

// KeepAll sends the entire history unchanged. The safe default.
type KeepAll struct{}

func (KeepAll) Assemble(h []provider.Message) []provider.Message { return h }

// Window keeps the first message (the task) plus a suffix of recent messages,
// bounded by MaxMessages. It is pairing-safe: the suffix never starts with an
// orphan tool_result whose tool_use was trimmed away (providers reject that).
type Window struct {
	MaxMessages int
}

func (w Window) Assemble(h []provider.Message) []provider.Message {
	if w.MaxMessages <= 1 || len(h) <= w.MaxMessages {
		return h
	}
	head := h[:1]
	tail := h[len(h)-(w.MaxMessages-1):]
	// Drop leading tool_result messages so the suffix can't begin with a
	// tool_result whose matching tool_use was dropped. (Within our loop an
	// assistant tool_use always immediately precedes its tool message, so once
	// the suffix starts clean, every tool message in it is properly paired.)
	for len(tail) > 0 && tail[0].Role == "tool" {
		tail = tail[1:]
	}
	out := make([]provider.Message, 0, len(head)+len(tail))
	out = append(out, head...)
	out = append(out, tail...)
	return out
}

// portableToolIDRe is the conservative tool-call id charset every provider
// accepts on replay; Anthropic enforces exactly this pattern, so a history that
// carries anything else 400s the moment the session switches to claude.
var portableToolIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// portableToolID rewrites a vendor-minted tool-call id onto the portable
// charset. Deterministic, so a tool_use and its tool_result rewrite to the same
// id and stay paired; portable ids (including its own output) pass through
// untouched, keeping repair idempotent.
func portableToolID(id string) string {
	if portableToolIDRe.MatchString(id) {
		return id
	}
	h := fnv.New64a()
	h.Write([]byte(id))
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, id)
	if len(clean) > 40 {
		clean = clean[:40]
	}
	return fmt.Sprintf("call_%s_%x", clean, h.Sum64())
}

// RepairHistory makes a persisted history safe to replay. Histories can carry
// damage — a model emitting a malformed tool call (empty name), or a restart
// clipping a turn so an assistant tool_use has no tool result — and providers
// reject such requests outright, wedging the session forever. Repair drops
// tool_use blocks with no name (and messages left empty by that), rewrites
// tool ids minted by another vendor onto the portable charset (a mid-session
// provider switch otherwise replays ids the new provider rejects), and closes
// any dangling tool_use with a synthetic "interrupted" result so pairing holds.
func RepairHistory(h []provider.Message) []provider.Message {
	out := make([]provider.Message, 0, len(h))
	for i, m := range h {
		if m.Role == "assistant" {
			blocks := make([]provider.Block, 0, len(m.Content))
			for _, b := range m.Content {
				if b.Type == provider.BlockToolUse {
					if b.ToolUse == nil || b.ToolUse.Name == "" {
						continue // malformed call — unreplayable
					}
					if pid := portableToolID(b.ToolUse.ID); pid != b.ToolUse.ID {
						tu := *b.ToolUse
						tu.ID = pid
						b.ToolUse = &tu
					}
				}
				blocks = append(blocks, b)
			}
			if len(blocks) == 0 {
				continue // nothing valid left in this message
			}
			m.Content = blocks
		}
		if m.Role == "tool" {
			blocks := make([]provider.Block, len(m.Content))
			for j, b := range m.Content {
				if b.Type == provider.BlockToolResult && b.ToolResult != nil {
					if pid := portableToolID(b.ToolResult.ToolUseID); pid != b.ToolResult.ToolUseID {
						tr := *b.ToolResult
						tr.ToolUseID = pid
						b.ToolResult = &tr
					}
				}
				blocks[j] = b
			}
			m.Content = blocks
		}
		out = append(out, m)

		if m.Role != "assistant" {
			continue
		}
		// A tool_use must be answered by results in the next message; when a
		// clipped turn left it dangling, answer it synthetically.
		var open []provider.Block
		for _, b := range m.Content {
			if b.Type == provider.BlockToolUse {
				open = append(open, b)
			}
		}
		if len(open) == 0 || (i+1 < len(h) && h[i+1].Role == "tool") {
			continue
		}
		results := make([]provider.Block, 0, len(open))
		for _, b := range open {
			results = append(results, provider.Block{
				Type: provider.BlockToolResult,
				ToolResult: &provider.ToolResultBlock{
					ToolUseID: b.ToolUse.ID,
					Content:   "(tool call interrupted — no result)",
					IsError:   true,
				},
			})
		}
		out = append(out, provider.Message{Role: "tool", Content: results})
	}
	return out
}

// Permission is a gate decision for a tool call.
type Permission int

const (
	Allow Permission = iota
	Deny
)

// PermissionGate decides whether a tool call may run. Returning Deny blocks
// execution; the model receives an error result and can adapt. This is the seam
// for confirmation prompts, allow/deny policy, and sandboxing.
type PermissionGate func(ctx context.Context, toolName string, input json.RawMessage) Permission
