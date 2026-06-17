package connector

import (
	"context"
	"fmt"

	"github.com/flakerimi/harness/tool"
)

// Native is a connector backed by in-process Go tools. It's always connected;
// use it to group the harness's built-in tools (filesystem, web_fetch, …) so
// they appear alongside external connectors in introspection.
type Native struct {
	name  string
	tools []tool.Tool
}

// NewNative builds a native connector from a name and a set of tools.
func NewNative(name string, tools ...tool.Tool) *Native {
	return &Native{name: name, tools: tools}
}

func (n *Native) Name() string { return n.name }

func (n *Native) Status(context.Context) Status {
	return Status{Connected: true, Detail: fmt.Sprintf("%d built-in tools", len(n.tools))}
}

func (n *Native) Tools(context.Context) ([]tool.Tool, error) {
	return n.tools, nil
}
