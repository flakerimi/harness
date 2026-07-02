package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flakerimi/harness/tool"
)

func TestConfirmWritesGatesOnlyMutatingTools(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(tool.ReadFile{})
	reg.Register(tool.WriteFile{})

	var asked []string
	allow := false
	gate := ConfirmWrites(reg, func(name, detail string) bool {
		asked = append(asked, name)
		return allow
	})
	ctx := context.Background()
	input := json.RawMessage(`{"path":"x"}`)

	// Reads pass without consulting the callback.
	if got := gate(ctx, "read_file", input); got != Allow {
		t.Errorf("read_file = %v, want Allow", got)
	}
	// Unknown tools (e.g. delegate on the orchestrator) pass through.
	if got := gate(ctx, "delegate", input); got != Allow {
		t.Errorf("unknown tool = %v, want Allow", got)
	}
	if len(asked) != 0 {
		t.Fatalf("callback consulted for non-writes: %v", asked)
	}

	// Writes ask, and the answer decides.
	if got := gate(ctx, "write_file", input); got != Deny {
		t.Errorf("write_file denied by callback = %v, want Deny", got)
	}
	allow = true
	if got := gate(ctx, "write_file", input); got != Allow {
		t.Errorf("write_file approved by callback = %v, want Allow", got)
	}
	if len(asked) != 2 {
		t.Errorf("callback should have been consulted twice, got %v", asked)
	}
}
