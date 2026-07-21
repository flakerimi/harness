// Package channel is the delivery seam: how finished text leaves the harness
// and reaches a person. A Deliverer pushes text to one destination over one
// transport; a Registry resolves "kind:dest" targets ("telegram:123",
// "webhook:https://…") to the Deliverer registered for that kind — the same
// role the provider registry plays for model slugs, so schedulers and surfaces
// stay ignorant of concrete channels. Kinds nothing registered fall through to
// an optional Fallback (the exec-plugin escape hatch), keeping the set
// extensible from outside the binary. app.Deliverers wires the built-ins.
package channel

import (
	"context"
	"fmt"
	"strings"
)

// Deliverer pushes text to a destination over one transport. dest is the
// kind-specific address — a chat id, a URL, a profile name.
type Deliverer interface {
	Deliver(ctx context.Context, dest, text string) error
}

// DelivererFunc adapts a function to the Deliverer interface.
type DelivererFunc func(ctx context.Context, dest, text string) error

func (f DelivererFunc) Deliver(ctx context.Context, dest, text string) error {
	return f(ctx, dest, text)
}

// Registry maps deliver kinds to Deliverers, preserving registration order.
// Re-registering a kind replaces it in place. The zero value is not usable;
// construct with NewRegistry.
type Registry struct {
	kinds map[string]Deliverer
	order []string

	// Fallback, when set, is consulted for kinds with no registered
	// Deliverer. It reports whether it recognized the kind; false falls
	// through to the unknown-kind error.
	Fallback func(ctx context.Context, kind, dest, text string) (bool, error)
}

func NewRegistry() *Registry {
	return &Registry{kinds: map[string]Deliverer{}}
}

func (r *Registry) Register(kind string, d Deliverer) {
	if _, ok := r.kinds[kind]; !ok {
		r.order = append(r.order, kind)
	}
	r.kinds[kind] = d
}

// Kinds returns the registered kinds in registration order.
func (r *Registry) Kinds() []string {
	return append([]string(nil), r.order...)
}

// Deliver sends text to one or more "kind:dest" targets separated by "|"
// ("telegram:123|push:personal" reaches both). Empty text is a no-op; with
// multiple targets each is attempted and the first error is reported.
func (r *Registry) Deliver(ctx context.Context, target, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if targets := strings.Split(target, "|"); len(targets) > 1 {
		var firstErr error
		for _, t := range targets {
			if t = strings.TrimSpace(t); t == "" {
				continue
			}
			if err := r.Deliver(ctx, t, text); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
	kind, dest, ok := strings.Cut(target, ":")
	if !ok || dest == "" {
		return fmt.Errorf("bad deliver target %q (want kind:dest, e.g. telegram:12345)", target)
	}
	if d, ok := r.kinds[kind]; ok {
		return d.Deliver(ctx, dest, text)
	}
	if r.Fallback != nil {
		if handled, err := r.Fallback(ctx, kind, dest, text); handled {
			return err
		}
	}
	return fmt.Errorf("unknown deliver kind %q (built-in: %s; no plugin advertises it)", kind, strings.Join(r.order, ", "))
}
