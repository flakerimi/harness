// Package connector is the generic integration layer. A Connector is a named
// source of tools with a connection status — the harness never hardcodes a
// specific vendor (Google, Microsoft, …); those arrive as connectors, and the
// harness exposes which ones are connected and what tools they provide.
//
// Two connector kinds are expected: native (built-in tools) and MCP (any MCP
// server, added later). Both satisfy this one interface, so the agent and the
// `connectors` introspection command treat them uniformly.
package connector

import (
	"context"
	"fmt"

	"github.com/flakerimi/harness/tool"
)

// Status reports whether a connector is usable right now.
type Status struct {
	Connected bool
	Detail    string // human-readable, e.g. "3 tools" or "needs login"
}

// Connector is a named, introspectable source of tools.
type Connector interface {
	Name() string
	Status(ctx context.Context) Status
	Tools(ctx context.Context) ([]tool.Tool, error)
}

// Registry holds the connectors wired into a harness instance.
type Registry struct {
	conns []Connector
}

// NewRegistry returns an empty connector registry.
func NewRegistry() *Registry { return &Registry{} }

// Add registers a connector.
func (r *Registry) Add(c Connector) { r.conns = append(r.conns, c) }

// Connectors returns the registered connectors in registration order.
func (r *Registry) Connectors() []Connector { return r.conns }

// Tools aggregates every connector's tools into a single tool.Registry for the
// agent. A connector that fails to enumerate its tools fails the whole build —
// callers that want best-effort behavior can iterate Connectors() themselves.
func (r *Registry) Tools(ctx context.Context) (*tool.Registry, error) {
	reg := tool.NewRegistry()
	for _, c := range r.conns {
		ts, err := c.Tools(ctx)
		if err != nil {
			return nil, fmt.Errorf("connector %q: %w", c.Name(), err)
		}
		for _, t := range ts {
			reg.Register(t)
		}
	}
	return reg, nil
}
