// Package tool defines the harness tool system: the Tool extension point, a
// Registry the agent dispatches through, and Env — the mediated handle a tool
// uses to touch the world. Routing every side effect through Env is what lets
// the harness sandbox, permission, and audit tools without each tool reaching
// for os/exec directly.
package tool

import (
	"context"
	"encoding/json"
)

// Tool is the primary extension point. Implementations are pure values; all
// state and I/O come through the Env passed to Run.
type Tool interface {
	Spec() Spec
	Run(ctx context.Context, input json.RawMessage, env *Env) (Result, error)
}

// Spec is a tool's advertised contract: name, description, and a JSON Schema
// for its input. The agent layer translates these into provider tool defs.
type Spec struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// Result is a tool's output, handed back to the model as a tool result.
type Result struct {
	Content string
	IsError bool
}

// Registry holds the tools available to an agent.
type Registry struct {
	tools map[string]Tool
	order []string
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register adds a tool, keyed by its Spec().Name. A later registration with the
// same name replaces the earlier one but keeps its position.
func (r *Registry) Register(t Tool) {
	name := t.Spec().Name
	if _, exists := r.tools[name]; !exists {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
}

// Get looks up a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Specs returns the registered tools' specs in registration order.
func (r *Registry) Specs() []Spec {
	specs := make([]Spec, 0, len(r.order))
	for _, name := range r.order {
		specs = append(specs, r.tools[name].Spec())
	}
	return specs
}

// All returns the registered tools in registration order.
func (r *Registry) All() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

// Env is the mediated view of the world handed to a tool. It exposes a
// filesystem root and the identity's persistent workspace; exec, network, and
// a permission callback land here next, so tools never widen their own blast
// radius.
type Env struct {
	Root      string // working-directory root for filesystem tools
	Workspace string // the identity's persistent home for files ("" = none); on remote surfaces Root == Workspace
}
