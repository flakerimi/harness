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

// TokenSource yields short-lived OAuth access tokens (e.g. from a refresh
// flow). When set on an Anthropic provider, the harness authenticates with
// Authorization: Bearer + the oauth beta header instead of x-api-key.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Anthropic adapts the Claude Messages API to the neutral Provider interface.
// It uses the raw HTTP + SSE wire protocol (no SDK dependency) so the harness
// stays self-contained.
type Anthropic struct {
	APIKey  string
	BaseURL string // default https://api.anthropic.com
	Version string // anthropic-version header, default 2023-06-01
	HTTP    *http.Client
	Tokens  TokenSource // when set, OAuth Bearer auth is used instead of APIKey
}

// WithOAuth switches the provider to OAuth Bearer authentication.
func (a *Anthropic) WithOAuth(src TokenSource) *Anthropic {
	a.Tokens = src
	return a
}

// NewAnthropic builds an Anthropic provider from an API key.
func NewAnthropic(apiKey string) *Anthropic {
	return &Anthropic{
		APIKey:  apiKey,
		BaseURL: "https://api.anthropic.com",
		Version: "2023-06-01",
		HTTP:    http.DefaultClient,
	}
}

func (a *Anthropic) Name() string { return "anthropic" }

func (a *Anthropic) Stream(ctx context.Context, req Request, emit func(Event)) error {
	body, err := json.Marshal(a.buildBody(req))
	if err != nil {
		return fmt.Errorf("anthropic: marshal body: %w", err)
	}

	url := strings.TrimRight(a.BaseURL, "/") + "/v1/messages"
	if a.Tokens != nil {
		// Claude Pro/Max OAuth requires the ?beta=true query param.
		url += "?beta=true"
	}
	newReq := func() (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "text/event-stream")
		r.Header.Set("anthropic-version", a.Version)
		if a.Tokens != nil {
			access, tokErr := a.Tokens.Token(ctx)
			if tokErr != nil {
				return nil, fmt.Errorf("anthropic: oauth token: %w", tokErr)
			}
			r.Header.Set("Authorization", "Bearer "+access)
			r.Header.Set("anthropic-beta", "oauth-2025-04-20")
			r.Header.Set("User-Agent", "harness/0.1")
		} else {
			r.Header.Set("x-api-key", a.APIKey)
		}
		return r, nil
	}

	resp, err := httpDoRetry(ctx, a.HTTP, newReq)
	if err != nil {
		return fmt.Errorf("anthropic: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("anthropic: http %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	return parseSSE(resp.Body, func(data string) error {
		a.handleEvent(data, emit)
		return nil
	})
}

// claudeCodeSystemPrompt is the identifier Anthropic requires as the first
// system block when authenticating with a Claude Pro/Max OAuth token. Without
// it (plus metadata.user_id and ?beta=true), the API rejects the request.
const claudeCodeSystemPrompt = "You are Claude Code, Anthropic's official CLI for Claude."

func (a *Anthropic) buildBody(req Request) map[string]any {
	useOAuth := a.Tokens != nil
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}
	body := map[string]any{
		"model":      req.Model,
		"max_tokens": maxTokens,
		"stream":     true,
		"messages":   anthropicMessages(req.Messages),
	}
	if useOAuth {
		body["metadata"] = map[string]any{"user_id": "harness"}
	}
	if sys := systemBlocks(req.System, useOAuth, req.HasCap(CapCaching)); sys != nil {
		body["system"] = sys
	}

	if len(req.Tools) > 0 && req.HasCap(CapTools) {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.InputSchema
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			tools = append(tools, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": schema,
			})
		}
		body["tools"] = tools
	}
	return body
}

// systemBlocks builds the system field. On OAuth the Claude Code identifier is
// always block #1; the user's system prompt follows. With caching, a
// cache_control breakpoint is placed on the last system block.
func systemBlocks(userSystem string, useOAuth, caching bool) any {
	if !useOAuth {
		if userSystem == "" {
			return nil
		}
		if !caching {
			return userSystem
		}
		return []map[string]any{{
			"type":          "text",
			"text":          userSystem,
			"cache_control": map[string]any{"type": "ephemeral"},
		}}
	}

	blocks := []map[string]any{{"type": "text", "text": claudeCodeSystemPrompt}}
	if userSystem != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": userSystem})
	}
	if caching {
		blocks[len(blocks)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
	}
	return blocks
}

// anthropicMessages translates neutral messages into Anthropic's content-block
// shape. The "tool" role becomes a user message carrying tool_result blocks.
func anthropicMessages(msgs []Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		role := m.Role
		if role == "tool" {
			role = "user"
		}
		content := make([]map[string]any, 0, len(m.Content))
		for _, b := range m.Content {
			switch b.Type {
			case BlockText:
				content = append(content, map[string]any{"type": "text", "text": b.Text})
			case BlockToolUse:
				if b.ToolUse != nil {
					input := b.ToolUse.Input
					if input == nil {
						input = map[string]any{}
					}
					content = append(content, map[string]any{
						"type":  "tool_use",
						"id":    b.ToolUse.ID,
						"name":  b.ToolUse.Name,
						"input": input,
					})
				}
			case BlockToolResult:
				if b.ToolResult != nil {
					content = append(content, map[string]any{
						"type":        "tool_result",
						"tool_use_id": b.ToolResult.ToolUseID,
						"content":     b.ToolResult.Content,
						"is_error":    b.ToolResult.IsError,
					})
				}
			}
		}
		out = append(out, map[string]any{"role": role, "content": content})
	}
	return out
}

type anthropicSSE struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Message *struct {
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`
	Usage *anthropicUsage `json:"usage"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (a *Anthropic) handleEvent(data string, emit func(Event)) {
	var e anthropicSSE
	if err := json.Unmarshal([]byte(data), &e); err != nil {
		return // skip unparseable lines (e.g. ping events)
	}
	switch e.Type {
	case "content_block_start":
		if e.ContentBlock != nil && e.ContentBlock.Type == "tool_use" {
			emit(Event{
				Type:      EventToolUseStart,
				Index:     e.Index,
				ToolUseID: e.ContentBlock.ID,
				ToolName:  e.ContentBlock.Name,
			})
		}
	case "content_block_delta":
		if e.Delta == nil {
			return
		}
		switch e.Delta.Type {
		case "text_delta":
			emit(Event{Type: EventTextDelta, Index: e.Index, TextDelta: e.Delta.Text})
		case "input_json_delta":
			emit(Event{Type: EventToolUseDelta, Index: e.Index, InputDelta: e.Delta.PartialJSON})
		}
	case "content_block_stop":
		emit(Event{Type: EventBlockStop, Index: e.Index})
	case "message_delta":
		if e.Usage != nil {
			emit(Event{Type: EventUsage, Usage: Usage{OutputTokens: e.Usage.OutputTokens}})
		}
		if e.Delta != nil && e.Delta.StopReason != "" {
			emit(Event{Type: EventStop, StopReason: e.Delta.StopReason})
		}
	}
}
