// Package plugin runs dropped-executable plugins — the zero-framework
// extension seam. A plugin is any executable in a plugins dir that answers a
// three-verb contract, writable in bash, Python, or anything else:
//
//	<exe> spec                    → JSON manifest on stdout (tools + deliver kinds)
//	<exe> run <tool>              ← tool input JSON on stdin → output on stdout; exit≠0 = error (stderr is the message)
//	<exe> deliver <kind> <dest>   ← text on stdin; exit≠0 = error
//
// Plugin tools join the agent through the connector layer, namespaced
// (<plugin>__<tool>) so they can't shadow built-ins, and a manifest's
// "writes": true marks a tool mutating so the permission gate covers it.
// Deliver kinds extend "-deliver" targets beyond the built-ins — a plugin
// advertising "sms" makes "sms:+1555..." work everywhere Deliver does. Where
// MCP suits full servers with sessions and state, a plugin is one file you
// drop in a directory.
package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/flakerimi/harness/connector"
	"github.com/flakerimi/harness/tool"
)

// Manifest is what `<exe> spec` prints: the plugin's advertised capabilities.
type Manifest struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Tools       []ToolSpec `json:"tools,omitempty"`
	Delivers    []string   `json:"delivers,omitempty"` // deliver kinds, e.g. ["sms"]
}

// ToolSpec describes one tool a plugin provides.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	Writes      bool           `json:"writes,omitempty"` // mutating → permission-gated
}

// Plugin is a discovered executable and its manifest.
type Plugin struct {
	Path     string
	Manifest Manifest
}

const (
	specTimeout    = 5 * time.Second
	runTimeout     = 2 * time.Minute
	deliverTimeout = time.Minute
	maxOutput      = 100_000 // bytes of stdout handed back to the model
)

// Discover scans dirs (in priority order) for executable files and loads each
// one's manifest. The first plugin claiming a name wins; failures are returned
// as warnings, never fatal — a broken plugin shouldn't take the harness down.
func Discover(ctx context.Context, dirs ...string) ([]Plugin, []error) {
	var (
		out  []Plugin
		errs []error
		seen = map[string]bool{}
	)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // absent dirs are the normal case
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil || info.Mode()&0o111 == 0 {
				continue // not executable
			}
			path := filepath.Join(dir, e.Name())
			p, err := load(ctx, path)
			if err != nil {
				errs = append(errs, fmt.Errorf("plugin %s: %w", path, err))
				continue
			}
			if seen[p.Manifest.Name] {
				errs = append(errs, fmt.Errorf("plugin %s: name %q already provided by an earlier dir", path, p.Manifest.Name))
				continue
			}
			seen[p.Manifest.Name] = true
			out = append(out, p)
		}
	}
	return out, errs
}

// load runs `<exe> spec` and parses the manifest.
func load(ctx context.Context, path string) (Plugin, error) {
	ctx, cancel := context.WithTimeout(ctx, specTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, "spec")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return Plugin{}, fmt.Errorf("spec: %s", firstNonEmpty(strings.TrimSpace(stderr.String()), err.Error()))
	}
	var m Manifest
	if err := json.Unmarshal(stdout.Bytes(), &m); err != nil {
		return Plugin{}, fmt.Errorf("spec: bad manifest JSON: %w", err)
	}
	if m.Name == "" {
		m.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return Plugin{Path: path, Manifest: m}, nil
}

// Deliver pipes text to `<exe> deliver <kind> <dest>`.
func (p Plugin) Deliver(ctx context.Context, kind, dest, text string) error {
	ctx, cancel := context.WithTimeout(ctx, deliverTimeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, p.Path, "deliver", kind, dest)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("plugin %s deliver: %s", p.Manifest.Name, firstNonEmpty(strings.TrimSpace(stderr.String()), err.Error()))
	}
	return nil
}

// FindDeliverer returns the first plugin advertising the deliver kind.
func FindDeliverer(plugins []Plugin, kind string) (Plugin, bool) {
	for _, p := range plugins {
		if slices.Contains(p.Manifest.Delivers, kind) {
			return p, true
		}
	}
	return Plugin{}, false
}

// Conn adapts a Plugin to the connector interface, so its tools join the agent
// like any other integration (namespaced by the connector layer).
type Conn struct{ p Plugin }

// New wraps a discovered plugin as a connector.
func New(p Plugin) *Conn { return &Conn{p: p} }

func (c *Conn) Name() string { return c.p.Manifest.Name }

func (c *Conn) Status(ctx context.Context) connector.Status {
	detail := fmt.Sprintf("plugin · %d tool(s)", len(c.p.Manifest.Tools))
	if len(c.p.Manifest.Delivers) > 0 {
		detail += fmt.Sprintf(" · delivers %s", strings.Join(c.p.Manifest.Delivers, ","))
	}
	return connector.Status{Connected: true, Detail: detail}
}

// Tools wraps each manifest tool as an exec-backed tool.Tool.
func (c *Conn) Tools(ctx context.Context) ([]tool.Tool, error) {
	out := make([]tool.Tool, 0, len(c.p.Manifest.Tools))
	for _, ts := range c.p.Manifest.Tools {
		out = append(out, execTool{path: c.p.Path, spec: ts})
	}
	return out, nil
}

// execTool is one plugin tool: Run shells out to `<exe> run <tool>` with the
// input JSON on stdin and the mediated Env passed as HARNESS_* variables.
type execTool struct {
	path string
	spec ToolSpec
}

func (t execTool) Spec() tool.Spec {
	schema := t.spec.InputSchema
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	return tool.Spec{
		Name:        t.spec.Name,
		Description: t.spec.Description,
		InputSchema: schema,
		Writes:      t.spec.Writes,
	}
}

func (t execTool) Run(ctx context.Context, input json.RawMessage, env *tool.Env) (tool.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, t.path, "run", t.spec.Name)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	root, workspace := "", ""
	if env != nil {
		root, workspace = env.Root, env.Workspace
	}
	cmd.Env = append(os.Environ(), "HARNESS_ROOT="+root, "HARNESS_WORKSPACE="+workspace)
	if err := cmd.Run(); err != nil {
		msg := firstNonEmpty(strings.TrimSpace(stderr.String()), err.Error())
		return tool.Result{Content: msg, IsError: true}, nil
	}
	out := stdout.String()
	if len(out) > maxOutput {
		out = out[:maxOutput] + "\n… (truncated)"
	}
	return tool.Result{Content: strings.TrimSpace(out)}, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
