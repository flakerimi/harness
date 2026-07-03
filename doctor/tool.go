package doctor

import (
	"context"
	"encoding/json"

	"github.com/flakerimi/harness/tool"
)

// NewTool lets the assistant diagnose its own connectivity — when tools time
// out or a provider misbehaves, running doctor tells it (and the user) exactly
// which wire is down instead of guessing. Read-only.
func NewTool(searxngURL, publicURL string) tool.Tool {
	return doctorTool{searxng: searxngURL, public: publicURL}
}

type doctorTool struct {
	searxng string
	public  string
}

func (doctorTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "doctor",
		Description: "Self-diagnose connectivity: probes internet egress, the chat channel, every configured model provider, web search, and the public URL. Run when tools are timing out or something feels broken — then report which wire is down instead of guessing.",
		InputSchema: map[string]any{"type": "object"},
	}
}

func (t doctorTool) Run(ctx context.Context, _ json.RawMessage, _ *tool.Env) (tool.Result, error) {
	checks := Run(ctx, t.searxng, t.public)
	out := Summary(checks)
	if !Healthy(checks) {
		out = "PROBLEMS FOUND:\n" + out
	}
	return tool.Result{Content: out}, nil
}
