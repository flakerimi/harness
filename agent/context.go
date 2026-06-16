package agent

import (
	"context"
	"encoding/json"

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
