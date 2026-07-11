// Package provider abstracts LLM backends behind one streaming interface.
//
// The agent loop sits ABOVE this interface and is provider-agnostic: Claude,
// OpenAI, or any OpenAI-compatible endpoint plug in through the same Provider.
// Tool calls and tool results travel as message blocks; the loop decides what
// to do with them. This is the single seam that makes the harness
// multi-provider — every adapter translates these neutral types to and from
// its own wire format, and nothing above the provider knows which vendor is
// in play.
package provider

import (
	"context"
	"fmt"
	"slices"
)

// Provider streams a single model turn.
type Provider interface {
	// Name returns the provider slug (e.g. "anthropic", "openai", "mock").
	Name() string
	// Stream runs one turn, invoking emit for each Event as it generates.
	// It returns when the turn ends (after emitting a "stop" Event) or on
	// error. Implementations must respect ctx cancellation.
	Stream(ctx context.Context, req Request, emit func(Event)) error
}

// Request is the provider-neutral input for one turn.
type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []Tool
	MaxTokens int

	// CapFlags are capabilities the caller resolved for this model/provider
	// (e.g. "caching", "tools"). Empty means "unknown" — a provider should
	// then default conservatively (no caching, no strict tools). When a flag
	// is present, the provider opts into the matching API feature.
	CapFlags []string
}

// Capability flags understood by the built-in adapters.
const (
	CapCaching    = "caching"    // provider may set prompt-cache breakpoints
	CapTools      = "tools"      // provider may advertise tools to the model
	CapStructured = "structured" // provider may enforce structured output
	CapVision     = "vision"     // model accepts image blocks (see BlockImage)
)

// HasCap reports whether the caller-resolved capabilities include flag.
func (r Request) HasCap(flag string) bool {
	return slices.Contains(r.CapFlags, flag)
}

// Message is one turn of conversation in neutral form.
type Message struct {
	Role    string  // "user" | "assistant" | "tool"
	Content []Block // text, tool_use, tool_result
}

// Block kinds.
const (
	BlockText       = "text"
	BlockImage      = "image"
	BlockToolUse    = "tool_use"
	BlockToolResult = "tool_result"
)

// Block is a single content block within a Message. Exactly one of the
// pointer fields is set, selected by Type.
type Block struct {
	Type       string
	Text       string
	Image      *ImageBlock
	ToolUse    *ToolUseBlock
	ToolResult *ToolResultBlock
}

// ImageBlock is image bytes supplied by the user. Data is raw, not base64:
// each adapter encodes it the way its vendor expects, so the neutral type
// stays free of wire formats. A provider that lacks CapVision must degrade the
// block via ImagePlaceholder rather than fail the request — sending an image to
// a text-only model is an API error, and silently dropping it would leave the
// model answering about something it was never shown.
type ImageBlock struct {
	MediaType string // e.g. "image/jpeg", "image/png", "image/gif", "image/webp"
	Data      []byte
}

// ImagePlaceholder is the text a blind model sees in place of an image. It says
// what was withheld and why, so the model can tell the user it cannot look
// instead of hallucinating a description.
func ImagePlaceholder(img *ImageBlock) string {
	if img == nil {
		return "[image omitted]"
	}
	return fmt.Sprintf("[image omitted: %s, %d bytes — this model has no vision capability]",
		img.MediaType, len(img.Data))
}

// ToolUseBlock is the model asking to call a tool.
type ToolUseBlock struct {
	ID    string
	Name  string
	Input map[string]any
}

// ToolResultBlock is the harness handing a tool's output back to the model.
type ToolResultBlock struct {
	ToolUseID string
	Content   string
	IsError   bool
}

// Tool is a tool definition advertised to the model. The agent layer builds
// these from its tool registry; providers translate them to vendor schemas.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// Event types streamed by a Provider.
const (
	EventTextDelta    = "text_delta"           // TextDelta carries the chunk
	EventToolUseStart = "tool_use_start"       // ToolUseID + ToolName begin a tool call
	EventToolUseDelta = "tool_use_input_delta" // InputDelta carries partial JSON args
	EventBlockStop    = "block_stop"           // current content block finished
	EventUsage        = "usage"                // Usage carries token accounting
	EventStop         = "stop"                 // StopReason ends the turn
)

// Stop reasons (neutral). Adapters map vendor-specific reasons onto these.
const (
	StopEndTurn   = "end_turn"
	StopToolUse   = "tool_use"
	StopMaxTokens = "max_tokens"
	StopRefusal   = "refusal"
)

// Event is what a provider streams as it generates. Consumers (the agent
// loop) accumulate these into Blocks and drive tool execution.
type Event struct {
	Type       string
	Index      int    // content-block index this event applies to, when relevant
	TextDelta  string // EventTextDelta
	ToolUseID  string // EventToolUseStart
	ToolName   string // EventToolUseStart
	InputDelta string // EventToolUseDelta — partial JSON for the tool input
	StopReason string // EventStop
	Usage      Usage  // EventUsage (cumulative; final on stop)
}

// Usage carries token accounting reported by the provider.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CacheRead    int
	CacheWrite   int
}
