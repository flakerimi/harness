package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/flakerimi/harness/connector"
	"github.com/flakerimi/harness/tool"
)

// Config describes one MCP server to launch as a connector.
type Config struct {
	Name    string   // connector name, e.g. "google-calendar"
	Command string   // executable to run
	Args    []string // arguments
	Env     []string // extra environment, "KEY=VALUE"
}

// Connector launches an MCP server on first use, lists its tools, and exposes
// them to the harness. It satisfies connector.Connector.
type Connector struct {
	cfg Config

	once   sync.Once
	client *Client
	tools  []tool.Tool
	status connector.Status
}

// New builds an MCP connector from a config (lazy — nothing starts until used).
func New(cfg Config) *Connector { return &Connector{cfg: cfg} }

func (m *Connector) Name() string { return m.cfg.Name }

func (m *Connector) Status(ctx context.Context) connector.Status {
	m.ensure(ctx)
	return m.status
}

func (m *Connector) Tools(ctx context.Context) ([]tool.Tool, error) {
	m.ensure(ctx)
	if !m.status.Connected {
		return nil, errors.New(m.status.Detail)
	}
	return m.tools, nil
}

// Close shuts down the underlying server process, if started.
func (m *Connector) Close() error {
	if m.client != nil {
		return m.client.Close()
	}
	return nil
}

// ensure starts + initializes the server and caches its tools exactly once.
func (m *Connector) ensure(ctx context.Context) {
	m.once.Do(func() {
		client, err := NewStdioClient(ctx, m.cfg.Command, m.cfg.Args, m.cfg.Env)
		if err != nil {
			m.status = connector.Status{Detail: "start failed: " + err.Error()}
			return
		}
		if err := client.Initialize(ctx); err != nil {
			m.status = connector.Status{Detail: "initialize failed: " + err.Error()}
			_ = client.Close()
			return
		}
		defs, err := client.ListTools(ctx)
		if err != nil {
			m.status = connector.Status{Detail: "tools/list failed: " + err.Error()}
			_ = client.Close()
			return
		}
		m.client = client
		m.tools = make([]tool.Tool, 0, len(defs))
		for _, d := range defs {
			m.tools = append(m.tools, &mcpTool{client: client, def: d})
		}
		m.status = connector.Status{
			Connected: true,
			Detail:    fmt.Sprintf("%d tools via %s", len(defs), m.cfg.Command),
		}
	})
}

// mcpTool adapts one server tool to the harness tool.Tool interface, proxying
// Run to a tools/call over the MCP client.
type mcpTool struct {
	client *Client
	def    ToolDef
}

func (t *mcpTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        t.def.Name,
		Description: t.def.Description,
		InputSchema: t.def.InputSchema,
	}
}

func (t *mcpTool) Run(ctx context.Context, input json.RawMessage, _ *tool.Env) (tool.Result, error) {
	var args map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return tool.Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
		}
	}
	text, isErr, err := t.client.CallTool(ctx, t.def.Name, args)
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	return tool.Result{Content: text, IsError: isErr}, nil
}
