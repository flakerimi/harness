package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
)

// mockServer is a tiny MCP server over r/w: it handles initialize, tools/list,
// and tools/call (an "echo" tool).
func mockServer(r io.Reader, w io.Writer) {
	br := bufio.NewReader(r)
	send := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = w.Write(append(b, '\n'))
	}
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			return
		}
		var msg map[string]any
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		method, _ := msg["method"].(string)
		id := msg["id"]
		switch method {
		case "initialize":
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "mock", "version": "1"},
				"capabilities":    map[string]any{},
			}})
		case "notifications/initialized":
			// notification — no response
		case "tools/list":
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"tools": []any{map[string]any{
					"name":        "echo",
					"description": "echoes the message",
					"inputSchema": map[string]any{"type": "object"},
				}},
			}})
		case "tools/call":
			params, _ := msg["params"].(map[string]any)
			args, _ := params["arguments"].(map[string]any)
			m, _ := args["msg"].(string)
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "echo: " + m}},
				"isError": false,
			}})
		}
	}
}

func mockClient(t *testing.T) *Client {
	t.Helper()
	c2sR, c2sW := io.Pipe() // client → server
	s2cR, s2cW := io.Pipe() // server → client
	go mockServer(c2sR, s2cW)
	return newClient(c2sW, s2cR, nil)
}

func TestClientHandshakeListCall(t *testing.T) {
	client := mockClient(t)
	ctx := context.Background()

	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v, want one 'echo'", tools)
	}

	text, isErr, err := client.CallTool(ctx, "echo", map[string]any{"msg": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if isErr || text != "echo: hi" {
		t.Fatalf("call → %q isErr=%v, want \"echo: hi\"", text, isErr)
	}
}

func TestMCPToolProxiesCall(t *testing.T) {
	client := mockClient(t)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	tl := &mcpTool{client: client, def: ToolDef{Name: "echo", Description: "echo"}}

	in, _ := json.Marshal(map[string]any{"msg": "yo"})
	res, err := tl.Run(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.Content != "echo: yo" {
		t.Fatalf("proxied result = %q isErr=%v, want \"echo: yo\"", res.Content, res.IsError)
	}
}
