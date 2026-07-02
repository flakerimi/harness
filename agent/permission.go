// Permission policies — concrete PermissionGate constructors. The gate itself
// is a seam on Options (run before every tool call); these are the shipped
// policies surfaces wire in.
package agent

import (
	"context"
	"encoding/json"

	"github.com/flakerimi/harness/tool"
)

// ConfirmWrites gates mutating tools (Spec.Writes) behind a confirmation
// callback; read-only tools pass through untouched. detail is the call's raw
// JSON input, clipped for display. This is the "reads are free, writes are
// confirmed" policy the CLI uses when working in the user's own directory —
// remote surfaces sandboxed in an identity workspace typically skip the gate.
func ConfirmWrites(reg *tool.Registry, confirm func(toolName, detail string) bool) PermissionGate {
	return func(ctx context.Context, name string, input json.RawMessage) Permission {
		t, ok := reg.Get(name)
		if !ok || !t.Spec().Writes {
			return Allow
		}
		detail := string(input)
		if len(detail) > 300 {
			detail = detail[:300] + "…"
		}
		if confirm(name, detail) {
			return Allow
		}
		return Deny
	}
}
