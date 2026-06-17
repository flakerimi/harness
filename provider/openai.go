package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAI adapts the Chat Completions API to the neutral Provider interface.
// Because the wire format is widely cloned, the same adapter serves OpenAI and
// any OpenAI-compatible upstream (Ollama, LM Studio, Together, Fireworks, …)
// by varying BaseURL.
type OpenAI struct {
	slug    string
	APIKey  string
	BaseURL string // e.g. https://api.openai.com/v1
	HTTP    *http.Client
}

// NewOpenAI builds an OpenAI-compatible provider. slug is the registry name,
// baseURL points at the API root (".../v1"), apiKey may be empty for local
// servers that don't authenticate.
func NewOpenAI(slug, baseURL, apiKey string) *OpenAI {
	return &OpenAI{slug: slug, APIKey: apiKey, BaseURL: baseURL, HTTP: http.DefaultClient}
}

func (o *OpenAI) Name() string { return o.slug }

func (o *OpenAI) Stream(ctx context.Context, req Request, emit func(Event)) error {
	body, err := json.Marshal(o.buildBody(req))
	if err != nil {
		return fmt.Errorf("openai: marshal body: %w", err)
	}

	url := strings.TrimRight(o.BaseURL, "/") + "/chat/completions"
	newReq := func() (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "text/event-stream")
		if o.APIKey != "" {
			r.Header.Set("Authorization", "Bearer "+o.APIKey)
		}
		return r, nil
	}

	resp, err := httpDoRetry(ctx, o.HTTP, newReq)
	if err != nil {
		return fmt.Errorf("openai: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("openai: http %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	// Tool calls stream by index; remember which indices we've announced so
	// EventToolUseStart fires exactly once per call.
	started := map[int]bool{}
	return parseSSE(resp.Body, func(data string) error {
		var chunk openAIChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil // tolerate keep-alive / non-JSON lines
		}
		if chunk.Usage != nil {
			emit(Event{Type: EventUsage, Usage: Usage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			}})
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				emit(Event{Type: EventTextDelta, TextDelta: ch.Delta.Content})
			}
			for _, tc := range ch.Delta.ToolCalls {
				if !started[tc.Index] {
					started[tc.Index] = true
					emit(Event{Type: EventToolUseStart, Index: tc.Index, ToolUseID: tc.ID, ToolName: tc.Function.Name})
				}
				if tc.Function.Arguments != "" {
					emit(Event{Type: EventToolUseDelta, Index: tc.Index, InputDelta: tc.Function.Arguments})
				}
			}
			if ch.FinishReason != "" {
				emit(Event{Type: EventBlockStop})
				emit(Event{Type: EventStop, StopReason: mapOpenAIFinish(ch.FinishReason)})
			}
		}
		return nil
	})
}

func (o *OpenAI) buildBody(req Request) map[string]any {
	body := map[string]any{
		"model":          req.Model,
		"stream":         true,
		"messages":       convertOpenAIMessages(req.System, req.Messages),
		"stream_options": map[string]any{"include_usage": true},
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 && req.HasCap(CapTools) {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			params := t.InputSchema
			if params == nil {
				params = map[string]any{"type": "object"}
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  params,
				},
			})
		}
		body["tools"] = tools
	}
	return body
}

// convertOpenAIMessages translates neutral messages into OpenAI's flat message
// list: assistant tool calls go under tool_calls; each tool result becomes its
// own message with role "tool".
func convertOpenAIMessages(system string, msgs []Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs)+1)
	if system != "" {
		out = append(out, map[string]any{"role": "system", "content": system})
	}
	for _, m := range msgs {
		switch m.Role {
		case "user":
			out = append(out, map[string]any{"role": "user", "content": joinText(m.Content)})
		case "assistant":
			msg := map[string]any{"role": "assistant"}
			if text := joinText(m.Content); text != "" {
				msg["content"] = text
			}
			var calls []map[string]any
			for _, b := range m.Content {
				if b.Type == BlockToolUse && b.ToolUse != nil {
					args, _ := json.Marshal(b.ToolUse.Input)
					calls = append(calls, map[string]any{
						"id":   b.ToolUse.ID,
						"type": "function",
						"function": map[string]any{
							"name":      b.ToolUse.Name,
							"arguments": string(args),
						},
					})
				}
			}
			if len(calls) > 0 {
				msg["tool_calls"] = calls
			}
			out = append(out, msg)
		case "tool":
			for _, b := range m.Content {
				if b.Type == BlockToolResult && b.ToolResult != nil {
					out = append(out, map[string]any{
						"role":         "tool",
						"tool_call_id": b.ToolResult.ToolUseID,
						"content":      b.ToolResult.Content,
					})
				}
			}
		}
	}
	return out
}

func joinText(blocks []Block) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == BlockText && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func mapOpenAIFinish(reason string) string {
	switch reason {
	case "tool_calls", "function_call":
		return StopToolUse
	case "length":
		return StopMaxTokens
	case "content_filter":
		return StopRefusal
	default:
		return StopEndTurn
	}
}

type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}
