// Package mcp is an MCP (Model Context Protocol) client connector. It speaks
// JSON-RPC 2.0 over a newline-delimited stdio transport, so any MCP server —
// calendar, mail, search, Notion, Slack — plugs into the harness as a connector
// with no harness code changes. The transport is split from the process spawn
// so the client is testable in-process against a mock server.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

const protocolVersion = "2025-06-18"

// Client is a minimal synchronous MCP client: one request in flight at a time.
type Client struct {
	w      io.Writer
	r      *bufio.Reader
	closer io.Closer

	mu     sync.Mutex
	nextID int
}

// newClient wraps an existing transport. Used directly by tests.
func newClient(w io.Writer, r io.Reader, closer io.Closer) *Client {
	return &Client{w: w, r: bufio.NewReader(r), closer: closer}
}

// NewStdioClient spawns an MCP server process and wraps its stdio. env entries
// are "KEY=VALUE" and are appended to the current environment.
func NewStdioClient(ctx context.Context, command string, args, env []string) (*Client, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = append(os.Environ(), env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", command, err)
	}
	return newClient(stdin, stdout, &procCloser{cmd: cmd, stdin: stdin}), nil
}

// ToolDef is an MCP tool advertised by tools/list.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Initialize performs the MCP handshake: initialize request + initialized
// notification.
func (c *Client) Initialize(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "harness", "version": "0.1"},
	})
	if err != nil {
		return err
	}
	return c.notify("notifications/initialized", map[string]any{})
}

// ListTools returns the server's advertised tools.
func (c *Client) ListTools(ctx context.Context) ([]ToolDef, error) {
	res, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("tools/list parse: %w", err)
	}
	return out.Tools, nil
}

// CallTool invokes a tool and returns its concatenated text content plus the
// server's isError flag.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, bool, error) {
	if args == nil {
		args = map[string]any{}
	}
	res, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", false, err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", false, fmt.Errorf("tools/call parse: %w", err)
	}
	var sb strings.Builder
	for _, blk := range out.Content {
		if blk.Type == "text" {
			sb.WriteString(blk.Text)
		}
	}
	return sb.String(), out.IsError, nil
}

// Close shuts down the transport.
func (c *Client) Close() error {
	if c.closer != nil {
		return c.closer.Close()
	}
	return nil
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.nextID++
	id := c.nextID
	if err := c.write(rpcMessage{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		return nil, err
	}

	for {
		line, err := c.r.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("mcp %s: read: %w", method, err)
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue // tolerate non-JSON noise
		}
		// Skip server-initiated requests/notifications (they carry a method).
		if msg.Method != "" || msg.ID == nil || *msg.ID != id {
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("mcp %s: %s", method, msg.Error.Message)
		}
		return msg.Result, nil
	}
}

func (c *Client) notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.write(rpcMessage{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *Client) write(msg rpcMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	_, err = c.w.Write(body)
	return err
}

type procCloser struct {
	cmd   *exec.Cmd
	stdin io.Closer
}

func (p *procCloser) Close() error {
	_ = p.stdin.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	return p.cmd.Wait()
}
