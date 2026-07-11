package provider

import (
	"context"
	"fmt"
	"strings"
)

// Mock is a dependency-free Provider for exercising the agent loop offline.
//
// Behavior: if tools are advertised and none has been called yet this turn,
// it calls the first tool (so the full call -> result -> answer loop runs);
// otherwise it streams a canned text reply echoing the last user message.
type Mock struct{}

// NewMock returns a Mock provider.
func NewMock() *Mock { return &Mock{} }

func (m *Mock) Name() string { return "mock" }

func (m *Mock) Stream(ctx context.Context, req Request, emit func(Event)) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Has a tool already been called in this conversation turn?
	calledTool := false
	for _, msg := range req.Messages {
		for _, b := range msg.Content {
			if b.Type == BlockToolResult {
				calledTool = true
			}
		}
	}

	// Demonstrate tool use: call the first available tool once.
	if len(req.Tools) > 0 && !calledTool {
		t := req.Tools[0]
		id := "mock_call_1"
		emit(Event{Type: EventToolUseStart, ToolUseID: id, ToolName: t.Name})
		emit(Event{Type: EventToolUseDelta, ToolUseID: id, InputDelta: "{}"})
		emit(Event{Type: EventBlockStop})
		emit(Event{Type: EventStop, StopReason: StopToolUse})
		return nil
	}

	// Otherwise stream a text answer. Images are called out so the loop's
	// multimodal path is observable without a vendor.
	reply := "[mock] you said: " + lastUserText(req)
	if n := lastUserImages(req); n > 0 {
		reply = fmt.Sprintf("[mock] saw %d image(s); you said: %s", n, lastUserText(req))
	}
	words := strings.Fields(reply)
	for _, w := range words {
		if err := ctx.Err(); err != nil {
			return err
		}
		emit(Event{Type: EventTextDelta, TextDelta: w + " "})
	}
	emit(Event{Type: EventBlockStop})
	emit(Event{Type: EventUsage, Usage: Usage{InputTokens: 8, OutputTokens: len(words)}})
	emit(Event{Type: EventStop, StopReason: StopEndTurn})
	return nil
}

// lastUserImages counts image blocks on the most recent user message.
func lastUserImages(req Request) int {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "user" {
			continue
		}
		n := 0
		for _, b := range req.Messages[i].Content {
			if b.Type == BlockImage && b.Image != nil {
				n++
			}
		}
		return n
	}
	return 0
}

func lastUserText(req Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "user" {
			continue
		}
		var parts []string
		for _, b := range req.Messages[i].Content {
			if b.Type == BlockText {
				parts = append(parts, b.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	return "(nothing)"
}
