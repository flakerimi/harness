package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// collect runs a provider Stream and returns the events it emits.
func collect(t *testing.T, p Provider, req Request) []Event {
	t.Helper()
	var evs []Event
	if err := p.Stream(context.Background(), req, func(e Event) { evs = append(evs, e) }); err != nil {
		t.Fatalf("stream: %v", err)
	}
	return evs
}

func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
}

func TestAnthropicStreamParsing(t *testing.T) {
	// A representative Claude SSE: text, a tool call streamed by index, usage,
	// and a stop reason.
	body := strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"search"}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"go\"}"}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
		`data: [DONE]`,
		"",
	}, "\n")
	srv := sseServer(t, body)
	defer srv.Close()

	a := &Anthropic{APIKey: "k", BaseURL: srv.URL, Version: "2023-06-01", HTTP: srv.Client()}
	evs := collect(t, a, Request{Model: "claude-x"})

	var text strings.Builder
	var sawToolStart, sawToolDelta, sawStop, sawUsage bool
	for _, e := range evs {
		switch e.Type {
		case EventTextDelta:
			text.WriteString(e.TextDelta)
		case EventToolUseStart:
			sawToolStart = e.ToolUseID == "t1" && e.ToolName == "search" && e.Index == 1
		case EventToolUseDelta:
			sawToolDelta = strings.Contains(e.InputDelta, `"q":"go"`)
		case EventStop:
			sawStop = e.StopReason == StopEndTurn
		case EventUsage:
			sawUsage = e.Usage.OutputTokens == 7
		}
	}
	if text.String() != "Hello world" {
		t.Errorf("text = %q, want %q", text.String(), "Hello world")
	}
	if !sawToolStart || !sawToolDelta || !sawStop || !sawUsage {
		t.Errorf("missing events: toolStart=%v toolDelta=%v stop=%v usage=%v", sawToolStart, sawToolDelta, sawStop, sawUsage)
	}
}

func TestOpenAIStreamParsing(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hi "}}]}`,
		`data: {"choices":[{"delta":{"content":"there"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"echo","arguments":"{\"n\":1}"}}]}}]}`,
		`data: {"choices":[{"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":3}}`,
		`data: [DONE]`,
		"",
	}, "\n")
	srv := sseServer(t, body)
	defer srv.Close()

	o := &OpenAI{slug: "openai", BaseURL: srv.URL, HTTP: srv.Client()}
	evs := collect(t, o, Request{Model: "gpt-x"})

	var text strings.Builder
	var sawToolStart, sawUsage bool
	for _, e := range evs {
		switch e.Type {
		case EventTextDelta:
			text.WriteString(e.TextDelta)
		case EventToolUseStart:
			sawToolStart = e.ToolName == "echo"
		case EventUsage:
			sawUsage = e.Usage.InputTokens == 11 && e.Usage.OutputTokens == 3
		}
	}
	if text.String() != "Hi there" {
		t.Errorf("text = %q, want %q", text.String(), "Hi there")
	}
	if !sawToolStart || !sawUsage {
		t.Errorf("missing events: toolStart=%v usage=%v", sawToolStart, sawUsage)
	}
}

func TestRetryOnTransient(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			w.Header().Set("Retry-After", "0") // keep the test fast
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, "slow down")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\ndata: [DONE]\n")
	}))
	defer srv.Close()

	o := &OpenAI{slug: "openai", BaseURL: srv.URL, HTTP: srv.Client()}
	evs := collect(t, o, Request{Model: "gpt-x"})
	if calls.Load() != 3 {
		t.Errorf("expected 2 retries then success (3 calls), got %d", calls.Load())
	}
	var text strings.Builder
	for _, e := range evs {
		if e.Type == EventTextDelta {
			text.WriteString(e.TextDelta)
		}
	}
	if text.String() != "ok" {
		t.Errorf("text after retry = %q, want %q", text.String(), "ok")
	}
}

func TestRetryGivesUpAndReportsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "down for maintenance")
	}))
	defer srv.Close()

	o := &OpenAI{slug: "openai", BaseURL: srv.URL, HTTP: srv.Client()}
	err := o.Stream(context.Background(), Request{Model: "gpt-x"}, func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("expected a 503 error after exhausting retries, got %v", err)
	}
}
