package agent

import (
	"strings"

	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/tool"
)

// Collector is a Handler that accumulates the streamed assistant text into a
// buffer, for surfaces that send one complete reply rather than streaming
// deltas — chat channels, scheduled runs, batch jobs. Tool, usage, route, and
// stop events are ignored; call Text after the turn for the full reply.
type Collector struct{ buf strings.Builder }

// Text returns the accumulated reply.
func (c *Collector) Text() string { return c.buf.String() }

func (c *Collector) OnText(delta string)                  { c.buf.WriteString(delta) }
func (c *Collector) OnToolStart(_, _ string)              {}
func (c *Collector) OnToolResult(_ string, _ tool.Result) {}
func (c *Collector) OnUsage(_ provider.Usage)             {}
func (c *Collector) OnStop(_ string)                      {}
